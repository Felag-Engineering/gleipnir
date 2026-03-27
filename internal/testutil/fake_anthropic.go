package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/rapp992/gleipnir/internal/llm"
)

// --- LLMClient mocks ---

// MockLLMClient implements llm.LLMClient by returning pre-canned responses in
// sequence. Tests that never call CreateMessage should use NewNoopLLMClient.
type MockLLMClient struct {
	mu        sync.Mutex
	Responses []*llm.MessageResponse
	calls     int
}

// NewMockLLMClient returns a MockLLMClient that returns responses in order.
func NewMockLLMClient(responses []*llm.MessageResponse) *MockLLMClient {
	return &MockLLMClient{Responses: responses}
}

// CreateMessage returns the next pre-canned response or an error when exhausted.
func (m *MockLLMClient) CreateMessage(_ context.Context, _ llm.MessageRequest) (*llm.MessageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls >= len(m.Responses) {
		return nil, fmt.Errorf("MockLLMClient: no more responses (called %d times, have %d)", m.calls+1, len(m.Responses))
	}
	resp := m.Responses[m.calls]
	m.calls++
	return resp, nil
}

// StreamMessage wraps CreateMessage and emits the complete response as a
// single MessageChunk on a buffered channel.
func (m *MockLLMClient) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	resp, err := m.CreateMessage(ctx, req)
	if err != nil {
		return nil, err
	}
	var chunk llm.MessageChunk
	if len(resp.Text) > 0 {
		text := resp.Text[0].Text
		chunk.Text = &text
	}
	if len(resp.ToolCalls) > 0 {
		chunk.ToolCall = &resp.ToolCalls[0]
	}
	chunk.StopReason = &resp.StopReason
	chunk.Usage = &resp.Usage
	ch := make(chan llm.MessageChunk, 1)
	ch <- chunk
	close(ch)
	return ch, nil
}

// ValidateOptions returns nil (no validation in mocks).
func (m *MockLLMClient) ValidateOptions(_ map[string]any) error { return nil }

// Calls returns the number of CreateMessage invocations.
func (m *MockLLMClient) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// noopLLMClient panics if CreateMessage is called. Use NewNoopLLMClient for
// tests that construct a BoundAgent but never trigger an API call.
type noopLLMClient struct{}

func (noopLLMClient) CreateMessage(_ context.Context, _ llm.MessageRequest) (*llm.MessageResponse, error) {
	panic("NoopLLMClient: CreateMessage called unexpectedly")
}

func (noopLLMClient) StreamMessage(_ context.Context, _ llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	panic("NoopLLMClient: StreamMessage called unexpectedly")
}

func (noopLLMClient) ValidateOptions(_ map[string]any) error { return nil }

// NewNoopLLMClient returns an llm.LLMClient that panics if CreateMessage or
// StreamMessage is called. Use in tests that never reach the API loop.
func NewNoopLLMClient() llm.LLMClient { return noopLLMClient{} }

// BlockingLLMTransport tracks CreateMessage calls and blocks until ctx is cancelled.
type BlockingLLMTransport struct {
	calls atomic.Int64
}

// Calls returns the number of CreateMessage invocations started so far.
func (t *BlockingLLMTransport) Calls() int { return int(t.calls.Load()) }

type blockingLLMClient struct {
	transport *BlockingLLMTransport
}

func (c *blockingLLMClient) CreateMessage(ctx context.Context, _ llm.MessageRequest) (*llm.MessageResponse, error) {
	c.transport.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *blockingLLMClient) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	_, err := c.CreateMessage(ctx, req)
	return nil, err
}

func (c *blockingLLMClient) ValidateOptions(_ map[string]any) error { return nil }

// NewBlockingLLMClient returns an llm.LLMClient that blocks until the context
// is cancelled. The transport reference lets callers synchronise on call starts.
func NewBlockingLLMClient() (llm.LLMClient, *BlockingLLMTransport) {
	t := &BlockingLLMTransport{}
	return &blockingLLMClient{transport: t}, t
}

// CapturingLLMTransport records each MessageRequest passed to CreateMessage.
type CapturingLLMTransport struct {
	mu       sync.Mutex
	requests []llm.MessageRequest
}

// Requests returns copies of all captured requests in call order.
func (t *CapturingLLMTransport) Requests() []llm.MessageRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]llm.MessageRequest, len(t.requests))
	copy(out, t.requests)
	return out
}

type capturingLLMClient struct {
	inner     *MockLLMClient
	transport *CapturingLLMTransport
}

func (c *capturingLLMClient) CreateMessage(ctx context.Context, req llm.MessageRequest) (*llm.MessageResponse, error) {
	c.transport.mu.Lock()
	c.transport.requests = append(c.transport.requests, req)
	c.transport.mu.Unlock()
	return c.inner.CreateMessage(ctx, req)
}

func (c *capturingLLMClient) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	return c.inner.StreamMessage(ctx, req)
}

func (c *capturingLLMClient) ValidateOptions(opts map[string]any) error {
	return c.inner.ValidateOptions(opts)
}

// NewCapturingLLMClient returns an llm.LLMClient that records every
// MessageRequest passed to CreateMessage and returns pre-canned responses.
func NewCapturingLLMClient(responses []*llm.MessageResponse) (llm.LLMClient, *CapturingLLMTransport) {
	inner := NewMockLLMClient(responses)
	transport := &CapturingLLMTransport{}
	return &capturingLLMClient{inner: inner, transport: transport}, transport
}

// MakeLLMTextResponse builds a *llm.MessageResponse with a single text block.
func MakeLLMTextResponse(text string, stopReason llm.StopReason, inputTokens, outputTokens int) *llm.MessageResponse {
	return &llm.MessageResponse{
		Text:       []llm.TextBlock{{Text: text}},
		StopReason: stopReason,
		Usage:      llm.TokenUsage{InputTokens: inputTokens, OutputTokens: outputTokens},
	}
}

// MakeLLMToolCallResponse builds a *llm.MessageResponse with a single tool call block.
func MakeLLMToolCallResponse(id, name string, input map[string]any, inputTokens, outputTokens int) *llm.MessageResponse {
	inputJSON, _ := json.Marshal(input)
	return &llm.MessageResponse{
		ToolCalls:  []llm.ToolCallBlock{{ID: id, Name: name, Input: json.RawMessage(inputJSON)}},
		StopReason: llm.StopReasonToolUse,
		Usage:      llm.TokenUsage{InputTokens: inputTokens, OutputTokens: outputTokens},
	}
}

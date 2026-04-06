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
	requests  []*llm.MessageRequest
	callIdx   int
}

// NewMockLLMClient returns a MockLLMClient that returns responses in order.
func NewMockLLMClient(responses ...*llm.MessageResponse) *MockLLMClient {
	return &MockLLMClient{Responses: responses}
}

// CreateMessage returns the next pre-canned response or an error when exhausted.
func (m *MockLLMClient) CreateMessage(_ context.Context, req llm.MessageRequest) (*llm.MessageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, &req)
	if m.callIdx >= len(m.Responses) {
		return nil, fmt.Errorf("MockLLMClient: no more responses (called %d times, have %d)", m.callIdx+1, len(m.Responses))
	}
	resp := m.Responses[m.callIdx]
	m.callIdx++
	return resp, nil
}

// StreamMessage wraps CreateMessage and delegates chunk-building to
// StubStreamFromResponse, keeping mock behavior consistent with real providers.
func (m *MockLLMClient) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	resp, err := m.CreateMessage(ctx, req)
	if err != nil {
		return nil, err
	}
	return llm.StubStreamFromResponse(resp), nil
}

// ValidateOptions returns nil (no validation in mocks).
func (m *MockLLMClient) ValidateOptions(_ map[string]any) error { return nil }

// ValidateModelName returns nil (no validation in mocks).
func (m *MockLLMClient) ValidateModelName(_ context.Context, _ string) error { return nil }

// ListModels returns nil (no model listing in mocks).
func (m *MockLLMClient) ListModels(_ context.Context) ([]llm.ModelInfo, error) { return nil, nil }

// InvalidateModelCache is a no-op for mocks.
func (m *MockLLMClient) InvalidateModelCache() {}

// Calls returns the number of CreateMessage invocations.
func (m *MockLLMClient) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callIdx
}

// Requests returns a copy of all captured requests in call order.
func (m *MockLLMClient) Requests() []*llm.MessageRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*llm.MessageRequest, len(m.requests))
	copy(out, m.requests)
	return out
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

func (noopLLMClient) ValidateOptions(_ map[string]any) error                { return nil }
func (noopLLMClient) ValidateModelName(_ context.Context, _ string) error   { return nil }
func (noopLLMClient) ListModels(_ context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (noopLLMClient) InvalidateModelCache()                                 {}

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

func (c *blockingLLMClient) ValidateOptions(_ map[string]any) error                { return nil }
func (c *blockingLLMClient) ValidateModelName(_ context.Context, _ string) error   { return nil }
func (c *blockingLLMClient) ListModels(_ context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (c *blockingLLMClient) InvalidateModelCache()                                 {}

// NewBlockingLLMClient returns an llm.LLMClient that blocks until the context
// is cancelled. The transport reference lets callers synchronise on call starts.
func NewBlockingLLMClient() (llm.LLMClient, *BlockingLLMTransport) {
	t := &BlockingLLMTransport{}
	return &blockingLLMClient{transport: t}, t
}

// errorLLMClient always returns the same error from CreateMessage and StreamMessage.
type errorLLMClient struct{ err error }

func (e *errorLLMClient) CreateMessage(_ context.Context, _ llm.MessageRequest) (*llm.MessageResponse, error) {
	return nil, e.err
}

func (e *errorLLMClient) StreamMessage(_ context.Context, _ llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	return nil, e.err
}

func (e *errorLLMClient) ValidateOptions(_ map[string]any) error                { return nil }
func (e *errorLLMClient) ValidateModelName(_ context.Context, _ string) error   { return nil }
func (e *errorLLMClient) ListModels(_ context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (e *errorLLMClient) InvalidateModelCache()                                 {}

// NewErrorLLMClient returns an llm.LLMClient that always returns err from
// CreateMessage and StreamMessage.
func NewErrorLLMClient(err error) llm.LLMClient { return &errorLLMClient{err: err} }

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

// MakeTextResponse builds a *llm.MessageResponse with sensible default token counts.
// Use MakeLLMTextResponse when you need explicit token counts.
func MakeTextResponse(text string) *llm.MessageResponse {
	return MakeLLMTextResponse(text, llm.StopReasonEndTurn, 10, 5)
}

// MakeToolCallResponse builds a *llm.MessageResponse for a tool call with sensible
// default token counts. Use MakeLLMToolCallResponse when you need explicit token counts.
// Note: params are (name, id, input) per the AC signature; MakeLLMToolCallResponse uses (id, name, input).
func MakeToolCallResponse(name, id string, input map[string]any) *llm.MessageResponse {
	return MakeLLMToolCallResponse(id, name, input, 10, 5)
}

// MockToolCall describes a single tool call for MakeMultiToolCallResponse.
type MockToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// MakeMultiToolCallResponse builds a *llm.MessageResponse containing multiple
// tool calls in a single response. StopReason is always StopReasonToolUse.
// Token counts default to 10 input / 5 output.
func MakeMultiToolCallResponse(calls []MockToolCall) *llm.MessageResponse {
	toolCalls := make([]llm.ToolCallBlock, 0, len(calls))
	for _, c := range calls {
		inputJSON, _ := json.Marshal(c.Input)
		toolCalls = append(toolCalls, llm.ToolCallBlock{
			ID:    c.ID,
			Name:  c.Name,
			Input: json.RawMessage(inputJSON),
		})
	}
	return &llm.MessageResponse{
		ToolCalls:  toolCalls,
		StopReason: llm.StopReasonToolUse,
		Usage:      llm.TokenUsage{InputTokens: 10, OutputTokens: 5},
	}
}

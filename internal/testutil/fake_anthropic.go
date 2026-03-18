package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// FakeAnthropicTransport is an http.RoundTripper that returns pre-canned
// anthropic.Message responses in sequence without hitting the real API.
type FakeAnthropicTransport struct {
	mu        sync.Mutex
	responses []*anthropic.Message
	calls     int
}

// RoundTrip returns the next pre-canned response as a JSON HTTP 200 reply.
// When all responses are exhausted it returns HTTP 400 (not 500) to prevent
// the SDK's retry logic from looping indefinitely.
func (t *FakeAnthropicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.calls >= len(t.responses) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"type":"invalid_request_error","message":"fakeAnthropicTransport: no more responses"}}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}

	msg := t.responses[t.calls]
	t.calls++

	body, err := json.Marshal(msg)
	if err != nil {
		panic("FakeAnthropicTransport: marshal response: " + err.Error())
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Request: req,
	}, nil
}

// Calls returns the number of RoundTrip invocations so far.
func (t *FakeAnthropicTransport) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

// NewFakeAnthropicClient creates an anthropic.Client backed by
// FakeAnthropicTransport. The fake-key prevents the SDK from reading
// ANTHROPIC_API_KEY from the environment; MaxRetries(0) prevents retry loops
// when the transport returns a 400 after all responses are exhausted.
func NewFakeAnthropicClient(responses []*anthropic.Message) *anthropic.Client {
	transport := &FakeAnthropicTransport{responses: responses}
	client := anthropic.NewClient(
		option.WithHTTPClient(&http.Client{Transport: transport}),
		option.WithAPIKey("fake-key"),
		option.WithMaxRetries(0),
	)
	return &client
}

// BlockingAnthropicTransport is an http.RoundTripper that blocks until the
// request's context is cancelled. It increments its counter before blocking
// so tests can synchronise on the API call starting.
type BlockingAnthropicTransport struct {
	calls atomic.Int64
}

// RoundTrip increments the call counter then blocks until ctx.Done().
func (t *BlockingAnthropicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// Calls returns the number of RoundTrip invocations started so far.
func (t *BlockingAnthropicTransport) Calls() int64 {
	return t.calls.Load()
}

// NewBlockingAnthropicClient returns a client + transport pair. The transport
// reference lets callers synchronise on and observe API call starts.
func NewBlockingAnthropicClient() (*anthropic.Client, *BlockingAnthropicTransport) {
	transport := &BlockingAnthropicTransport{}
	client := anthropic.NewClient(
		option.WithHTTPClient(&http.Client{Transport: transport}),
		option.WithAPIKey("fake-key"),
		option.WithMaxRetries(0),
	)
	return &client, transport
}

// CapturingAnthropicTransport wraps FakeAnthropicTransport and captures the
// raw request body bytes for each call. Tests can unmarshal the captured
// bodies to inspect what was sent to the API.
type CapturingAnthropicTransport struct {
	inner   *FakeAnthropicTransport
	mu      sync.Mutex
	bodies  [][]byte
}

// RoundTrip captures the request body then delegates to the inner transport.
func (t *CapturingAnthropicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))

	t.mu.Lock()
	t.bodies = append(t.bodies, body)
	t.mu.Unlock()

	return t.inner.RoundTrip(req)
}

// CapturedBodies returns copies of all captured request bodies in call order.
func (t *CapturingAnthropicTransport) CapturedBodies() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]byte, len(t.bodies))
	copy(out, t.bodies)
	return out
}

// NewCapturingAnthropicClient returns a client + transport pair. The transport
// captures raw request bodies so tests can inspect what was sent.
func NewCapturingAnthropicClient(responses []*anthropic.Message) (*anthropic.Client, *CapturingAnthropicTransport) {
	inner := &FakeAnthropicTransport{responses: responses}
	transport := &CapturingAnthropicTransport{inner: inner}
	client := anthropic.NewClient(
		option.WithHTTPClient(&http.Client{Transport: transport}),
		option.WithAPIKey("fake-key"),
		option.WithMaxRetries(0),
	)
	return &client, transport
}

// noopTransport panics if RoundTrip is called — used for tests that must not
// make any Anthropic API calls.
type noopTransport struct{}

func (noopTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	panic("NoopAnthropicClient: RoundTrip called unexpectedly")
}

// NoopAnthropicClient returns a client whose transport panics if called.
// Use this in tests that construct a BoundAgent but never trigger an API call.
func NoopAnthropicClient() *anthropic.Client {
	client := anthropic.NewClient(
		option.WithHTTPClient(&http.Client{Transport: noopTransport{}}),
		option.WithAPIKey("fake-key"),
		option.WithMaxRetries(0),
	)
	return &client
}

// MakeTextMessage constructs an anthropic.Message with a text content block
// via JSON unmarshal so that AsAny() (which inspects raw JSON) works correctly.
func MakeTextMessage(text string, stopReason anthropic.StopReason, inputTokens, outputTokens int64) *anthropic.Message {
	raw, _ := json.Marshal(map[string]any{
		"id":            "msg_test",
		"type":          "message",
		"role":          "assistant",
		"stop_reason":   string(stopReason),
		"stop_sequence": "",
		"model":         "claude-sonnet-4-6",
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"usage": map[string]any{
			"input_tokens":                inputTokens,
			"output_tokens":               outputTokens,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"service_tier":                "standard",
		},
	})
	var msg anthropic.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		panic("MakeTextMessage: " + err.Error())
	}
	return &msg
}

// MakeToolUseMessage constructs an anthropic.Message with a tool_use content
// block via JSON unmarshal so that AsAny() works correctly.
func MakeToolUseMessage(toolUseID, toolName string, input map[string]any, inputTokens, outputTokens int64) *anthropic.Message {
	inputJSON, _ := json.Marshal(input)
	raw, _ := json.Marshal(map[string]any{
		"id":            "msg_test",
		"type":          "message",
		"role":          "assistant",
		"stop_reason":   "tool_use",
		"stop_sequence": "",
		"model":         "claude-sonnet-4-6",
		"content": []map[string]any{
			{
				"type":  "tool_use",
				"id":    toolUseID,
				"name":  toolName,
				"input": json.RawMessage(inputJSON),
			},
		},
		"usage": map[string]any{
			"input_tokens":                inputTokens,
			"output_tokens":               outputTokens,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"service_tier":                "standard",
		},
	})
	var msg anthropic.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		panic("MakeToolUseMessage: " + err.Error())
	}
	return &msg
}

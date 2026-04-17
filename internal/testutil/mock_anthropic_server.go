package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/rapp992/gleipnir/internal/llm/anthropic"
)

// MockAnthropicServer is a configurable HTTP test server that mimics the
// Anthropic Messages API (POST /v1/messages). It supports both synchronous
// JSON responses and streaming SSE responses, matching Anthropic's event
// format. Tests queue canned responses via the constructor or AddResponse;
// the server dequeues them in order per request.
type MockAnthropicServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	queue   []MockAnthropicResponse
	reqs    []CapturedAnthropicRequest
	options MockServerOptions
}

// MockAnthropicResponse defines a single canned response the server returns.
// If Error is non-nil, the server returns an error response instead of content.
type MockAnthropicResponse struct {
	Content    []MockContentBlock
	StopReason string // "end_turn", "tool_use", "max_tokens"
	Usage      MockUsage
	Error      *MockErrorResponse
}

// MockContentBlock represents a single content block in the response.
// Set Type to "text" or "tool_use".
type MockContentBlock struct {
	Type  string          // "text" or "tool_use"
	Text  string          // populated for "text" blocks
	ID    string          // populated for "tool_use" blocks
	Name  string          // populated for "tool_use" blocks
	Input json.RawMessage // populated for "tool_use" blocks
}

// MockUsage holds token counts for the canned response.
type MockUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// MockErrorResponse triggers an error response with the given HTTP status.
type MockErrorResponse struct {
	StatusCode int
	Type       string // e.g. "rate_limit_error", "authentication_error"
	Message    string
}

// MockServerOptions configures optional behavior for the mock server.
type MockServerOptions struct {
	// Latency adds a delay before the server starts writing any response.
	Latency time.Duration
	// StreamDelay adds a delay between each SSE event when streaming.
	StreamDelay time.Duration
}

// CapturedAnthropicRequest holds the decoded fields from an incoming request,
// allowing tests to assert on what the client sent.
type CapturedAnthropicRequest struct {
	Model        string          `json:"model"`
	MaxTokens    int             `json:"max_tokens"`
	System       json.RawMessage `json:"system"`
	Messages     json.RawMessage `json:"messages"`
	Tools        json.RawMessage `json:"tools"`
	Stream       bool            `json:"stream"`
	Thinking     json.RawMessage `json:"thinking"`      // non-nil when thinking config is sent
	OutputConfig json.RawMessage `json:"output_config"` // non-nil when effort is set
}

// NewMockAnthropicServer creates and starts a test server pre-loaded with the
// given canned responses. The server is registered for cleanup via t.Cleanup.
func NewMockAnthropicServer(t testing.TB, responses ...MockAnthropicResponse) *MockAnthropicServer {
	t.Helper()
	s := &MockAnthropicServer{
		queue: append([]MockAnthropicResponse{}, responses...),
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(s.Close)
	return s
}

// WithOptions sets server options (latency, stream delay). Returns the server
// for method chaining.
func (s *MockAnthropicServer) WithOptions(opts MockServerOptions) *MockAnthropicServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.options = opts
	return s
}

// Client returns an AnthropicClient configured to use this mock server.
// Retries are disabled so error tests get immediate responses.
func (s *MockAnthropicServer) Client(t testing.TB) *anthropic.AnthropicClient {
	t.Helper()
	return anthropic.NewClient("test-key",
		option.WithBaseURL(s.server.URL),
		option.WithMaxRetries(0),
	)
}

// URL returns the base URL of the mock server.
func (s *MockAnthropicServer) URL() string {
	return s.server.URL
}

// Close shuts down the test server.
func (s *MockAnthropicServer) Close() {
	s.server.Close()
}

// Requests returns a copy of all captured requests in call order.
func (s *MockAnthropicServer) Requests() []CapturedAnthropicRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CapturedAnthropicRequest, len(s.reqs))
	copy(out, s.reqs)
	return out
}

// AddResponse appends a response to the end of the queue.
func (s *MockAnthropicServer) AddResponse(resp MockAnthropicResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, resp)
}

// --- Convenience constructors ---

// MockTextResponse creates a canned response with a single text block.
func MockTextResponse(text string) MockAnthropicResponse {
	return MockAnthropicResponse{
		Content: []MockContentBlock{
			{Type: "text", Text: text},
		},
		StopReason: "end_turn",
		Usage:      MockUsage{InputTokens: 10, OutputTokens: 5},
	}
}

// MockToolUseResponse creates a canned response with a single tool_use block.
func MockToolUseResponse(id, name string, input map[string]any) MockAnthropicResponse {
	inputJSON, _ := json.Marshal(input)
	return MockAnthropicResponse{
		Content: []MockContentBlock{
			{Type: "tool_use", ID: id, Name: name, Input: inputJSON},
		},
		StopReason: "tool_use",
		Usage:      MockUsage{InputTokens: 10, OutputTokens: 5},
	}
}

// MockErrorResp creates a canned error response.
func MockErrorResp(statusCode int, errType, message string) MockAnthropicResponse {
	return MockAnthropicResponse{
		Error: &MockErrorResponse{
			StatusCode: statusCode,
			Type:       errType,
			Message:    message,
		},
	}
}

// --- Internal handler ---

// handler processes each incoming request to /v1/messages.
func (s *MockAnthropicServer) handler(w http.ResponseWriter, r *http.Request) {
	// Capture the request body.
	var req CapturedAnthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"invalid JSON"}}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.reqs = append(s.reqs, req)

	// Dequeue the next response.
	if len(s.queue) == 0 {
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"type":"error","error":{"type":"api_error","message":"mock server: no more canned responses"}}`)
		return
	}
	resp := s.queue[0]
	s.queue = s.queue[1:]
	opts := s.options
	s.mu.Unlock()

	// Apply latency if configured.
	if opts.Latency > 0 {
		time.Sleep(opts.Latency)
	}

	// Error response.
	if resp.Error != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.Error.StatusCode)
		errBody, _ := json.Marshal(map[string]any{
			"type": "error",
			"error": map[string]string{
				"type":    resp.Error.Type,
				"message": resp.Error.Message,
			},
		})
		w.Write(errBody) //nolint:errcheck
		return
	}

	if req.Stream {
		s.writeStreamResponse(w, resp, opts.StreamDelay)
	} else {
		s.writeJSONResponse(w, resp)
	}
}

// writeJSONResponse writes a non-streaming Anthropic message response.
func (s *MockAnthropicServer) writeJSONResponse(w http.ResponseWriter, resp MockAnthropicResponse) {
	content := buildContentJSON(resp.Content)
	msg := map[string]any{
		"id":          "msg_mock_001",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-3-5-sonnet-20241022",
		"content":     content,
		"stop_reason": resp.StopReason,
		"usage": map[string]int{
			"input_tokens":  resp.Usage.InputTokens,
			"output_tokens": resp.Usage.OutputTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(msg) //nolint:errcheck
}

// writeStreamResponse writes SSE events matching Anthropic's streaming format.
func (s *MockAnthropicServer) writeStreamResponse(w http.ResponseWriter, resp MockAnthropicResponse, streamDelay time.Duration) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	writeSSE := func(event, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
		if streamDelay > 0 {
			time.Sleep(streamDelay)
		}
	}

	// message_start
	msgStart, _ := json.Marshal(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":          "msg_mock_001",
			"type":        "message",
			"role":        "assistant",
			"model":       "claude-3-5-sonnet-20241022",
			"content":     []any{},
			"stop_reason": nil,
			"usage": map[string]int{
				"input_tokens":  resp.Usage.InputTokens,
				"output_tokens": 0,
			},
		},
	})
	writeSSE("message_start", string(msgStart))

	// Emit content blocks.
	for i, block := range resp.Content {
		switch block.Type {
		case "text":
			s.writeTextBlockEvents(writeSSE, i, block.Text)
		case "tool_use":
			s.writeToolUseBlockEvents(writeSSE, i, block)
		}
	}

	// message_delta — carries stop_reason and final output token count.
	msgDelta, _ := json.Marshal(map[string]any{
		"type": "message_delta",
		"delta": map[string]string{
			"stop_reason": resp.StopReason,
		},
		"usage": map[string]int{
			"output_tokens": resp.Usage.OutputTokens,
		},
	})
	writeSSE("message_delta", string(msgDelta))

	// message_stop
	msgStop, _ := json.Marshal(map[string]any{
		"type": "message_stop",
	})
	writeSSE("message_stop", string(msgStop))
}

// writeTextBlockEvents emits the SSE events for a text content block:
// content_block_start, content_block_delta, content_block_stop.
func (s *MockAnthropicServer) writeTextBlockEvents(writeSSE func(string, string), index int, text string) {
	// content_block_start
	blockStart, _ := json.Marshal(map[string]any{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]string{
			"type": "text",
			"text": "",
		},
	})
	writeSSE("content_block_start", string(blockStart))

	// content_block_delta — send the full text as a single delta.
	blockDelta, _ := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]string{
			"type": "text_delta",
			"text": text,
		},
	})
	writeSSE("content_block_delta", string(blockDelta))

	// content_block_stop
	blockStop, _ := json.Marshal(map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
	writeSSE("content_block_stop", string(blockStop))
}

// writeToolUseBlockEvents emits the SSE events for a tool_use content block.
func (s *MockAnthropicServer) writeToolUseBlockEvents(writeSSE func(string, string), index int, block MockContentBlock) {
	// content_block_start — includes tool ID and name.
	blockStart, _ := json.Marshal(map[string]any{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    block.ID,
			"name":  block.Name,
			"input": map[string]any{},
		},
	})
	writeSSE("content_block_start", string(blockStart))

	// content_block_delta — send the full input JSON as a single delta.
	inputStr := string(block.Input)
	if inputStr == "" || inputStr == "null" {
		inputStr = "{}"
	}
	blockDelta, _ := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{
			"type":         "input_json_delta",
			"partial_json": inputStr,
		},
	})
	writeSSE("content_block_delta", string(blockDelta))

	// content_block_stop
	blockStop, _ := json.Marshal(map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
	writeSSE("content_block_stop", string(blockStop))
}

// buildContentJSON converts MockContentBlocks to the JSON structure Anthropic
// uses in non-streaming responses.
func buildContentJSON(blocks []MockContentBlock) []map[string]any {
	result := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			result = append(result, map[string]any{
				"type": "text",
				"text": b.Text,
			})
		case "tool_use":
			var input any
			if len(b.Input) > 0 {
				json.Unmarshal(b.Input, &input) //nolint:errcheck
			} else {
				input = map[string]any{}
			}
			result = append(result, map[string]any{
				"type":  "tool_use",
				"id":    b.ID,
				"name":  b.Name,
				"input": input,
			})
		}
	}
	return result
}

package testutil

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestMockAnthropicServer_NonStreamingText verifies that a non-streaming
// request returns a well-formed JSON response with a text block.
func TestMockAnthropicServer_NonStreamingText(t *testing.T) {
	srv := NewMockAnthropicServer(t, MockTextResponse("Hello, world!"))

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var msg map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if msg["role"] != "assistant" {
		t.Errorf("expected role=assistant, got %v", msg["role"])
	}
	if msg["stop_reason"] != "end_turn" {
		t.Errorf("expected stop_reason=end_turn, got %v", msg["stop_reason"])
	}
	content := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("expected type=text, got %v", block["type"])
	}
	if block["text"] != "Hello, world!" {
		t.Errorf("expected text='Hello, world!', got %v", block["text"])
	}
}

// TestMockAnthropicServer_NonStreamingToolUse verifies tool_use blocks in
// non-streaming mode.
func TestMockAnthropicServer_NonStreamingToolUse(t *testing.T) {
	srv := NewMockAnthropicServer(t, MockToolUseResponse("call_1", "get_weather", map[string]any{"city": "NYC"}))

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"weather?"}]}`
	resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	var msg map[string]any
	json.NewDecoder(resp.Body).Decode(&msg) //nolint:errcheck
	if msg["stop_reason"] != "tool_use" {
		t.Errorf("expected stop_reason=tool_use, got %v", msg["stop_reason"])
	}
	content := msg["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Errorf("expected type=tool_use, got %v", block["type"])
	}
	if block["name"] != "get_weather" {
		t.Errorf("expected name=get_weather, got %v", block["name"])
	}
	input := block["input"].(map[string]any)
	if input["city"] != "NYC" {
		t.Errorf("expected city=NYC, got %v", input["city"])
	}
}

// TestMockAnthropicServer_StreamingText verifies that streaming mode produces
// correct SSE events for a text response.
func TestMockAnthropicServer_StreamingText(t *testing.T) {
	srv := NewMockAnthropicServer(t, MockTextResponse("Hello from stream"))

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type=text/event-stream, got %s", ct)
	}

	events := parseSSEEvents(t, resp.Body)

	// Expected event sequence for a single text block:
	// message_start, content_block_start, content_block_delta, content_block_stop,
	// message_delta, message_stop
	expectedTypes := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}

	if len(events) != len(expectedTypes) {
		t.Fatalf("expected %d events, got %d: %v", len(expectedTypes), len(events), eventNames(events))
	}

	for i, want := range expectedTypes {
		if events[i].event != want {
			t.Errorf("event[%d]: expected %s, got %s", i, want, events[i].event)
		}
	}

	// Verify the text delta contains our text.
	var delta map[string]any
	json.Unmarshal([]byte(events[2].data), &delta) //nolint:errcheck
	d := delta["delta"].(map[string]any)
	if d["text"] != "Hello from stream" {
		t.Errorf("expected delta text='Hello from stream', got %v", d["text"])
	}

	// Verify message_delta carries stop_reason.
	var msgDelta map[string]any
	json.Unmarshal([]byte(events[4].data), &msgDelta) //nolint:errcheck
	dd := msgDelta["delta"].(map[string]any)
	if dd["stop_reason"] != "end_turn" {
		t.Errorf("expected stop_reason=end_turn, got %v", dd["stop_reason"])
	}
}

// TestMockAnthropicServer_StreamingToolUse verifies SSE events for a tool_use
// streaming response.
func TestMockAnthropicServer_StreamingToolUse(t *testing.T) {
	srv := NewMockAnthropicServer(t, MockToolUseResponse("call_1", "read_file", map[string]any{"path": "/tmp/x"}))

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"read it"}]}`
	resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	events := parseSSEEvents(t, resp.Body)

	// For a tool_use block, we expect:
	// message_start, content_block_start, content_block_delta, content_block_stop,
	// message_delta, message_stop
	if len(events) != 6 {
		t.Fatalf("expected 6 events, got %d: %v", len(events), eventNames(events))
	}

	// Verify content_block_start contains tool metadata.
	var blockStart map[string]any
	json.Unmarshal([]byte(events[1].data), &blockStart) //nolint:errcheck
	cb := blockStart["content_block"].(map[string]any)
	if cb["type"] != "tool_use" {
		t.Errorf("expected content_block type=tool_use, got %v", cb["type"])
	}
	if cb["name"] != "read_file" {
		t.Errorf("expected name=read_file, got %v", cb["name"])
	}

	// Verify the input_json_delta.
	var blockDelta map[string]any
	json.Unmarshal([]byte(events[2].data), &blockDelta) //nolint:errcheck
	d := blockDelta["delta"].(map[string]any)
	if d["type"] != "input_json_delta" {
		t.Errorf("expected delta type=input_json_delta, got %v", d["type"])
	}
}

// TestMockAnthropicServer_ErrorInjection verifies that the server returns
// error responses with the correct status code and body.
func TestMockAnthropicServer_ErrorInjection(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		errType    string
		message    string
	}{
		{"rate_limit", 429, "rate_limit_error", "rate limited"},
		{"auth_error", 401, "authentication_error", "invalid key"},
		{"server_error", 500, "api_error", "internal error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewMockAnthropicServer(t, MockErrorResp(tt.statusCode, tt.errType, tt.message))

			body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
			resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatalf("POST failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.statusCode {
				t.Errorf("expected status %d, got %d", tt.statusCode, resp.StatusCode)
			}

			var errResp map[string]any
			json.NewDecoder(resp.Body).Decode(&errResp) //nolint:errcheck
			errObj := errResp["error"].(map[string]any)
			if errObj["type"] != tt.errType {
				t.Errorf("expected error type=%s, got %v", tt.errType, errObj["type"])
			}
			if errObj["message"] != tt.message {
				t.Errorf("expected message=%s, got %v", tt.message, errObj["message"])
			}
		})
	}
}

// TestMockAnthropicServer_LatencySimulation verifies that the configured
// latency delay is applied before responding.
func TestMockAnthropicServer_LatencySimulation(t *testing.T) {
	latency := 50 * time.Millisecond
	srv := NewMockAnthropicServer(t, MockTextResponse("delayed")).
		WithOptions(MockServerOptions{Latency: latency})

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	start := time.Now()
	resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()

	if elapsed < latency {
		t.Errorf("expected at least %v delay, got %v", latency, elapsed)
	}
}

// TestMockAnthropicServer_RequestCapture verifies that requests are recorded
// for later assertion.
func TestMockAnthropicServer_RequestCapture(t *testing.T) {
	srv := NewMockAnthropicServer(t,
		MockTextResponse("resp1"),
		MockTextResponse("resp2"),
	)

	for _, msg := range []string{"first", "second"} {
		body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"` + msg + `"}]}`
		resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
		resp.Body.Close()
	}

	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(reqs))
	}
	if reqs[0].Model != "claude-3-5-sonnet-20241022" {
		t.Errorf("expected model=claude-3-5-sonnet-20241022, got %s", reqs[0].Model)
	}
	if reqs[0].MaxTokens != 100 {
		t.Errorf("expected max_tokens=100, got %d", reqs[0].MaxTokens)
	}
}

// TestMockAnthropicServer_ExhaustedQueue verifies that requesting beyond the
// canned responses returns a 500 error.
func TestMockAnthropicServer_ExhaustedQueue(t *testing.T) {
	srv := NewMockAnthropicServer(t, MockTextResponse("only one"))

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`

	// First request succeeds.
	resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("first request: expected 200, got %d", resp.StatusCode)
	}

	// Second request should fail with 500.
	resp2, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 500 {
		t.Errorf("second request: expected 500, got %d", resp2.StatusCode)
	}
}

// TestMockAnthropicServer_MixedContent verifies a response containing both
// text and tool_use blocks in a single response.
func TestMockAnthropicServer_MixedContent(t *testing.T) {
	inputJSON, _ := json.Marshal(map[string]any{"query": "test"})
	resp := MockAnthropicResponse{
		Content: []MockContentBlock{
			{Type: "text", Text: "Let me search for that."},
			{Type: "tool_use", ID: "call_1", Name: "search", Input: inputJSON},
		},
		StopReason: "tool_use",
		Usage:      MockUsage{InputTokens: 15, OutputTokens: 10},
	}
	srv := NewMockAnthropicServer(t, resp)

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"search"}]}`
	httpResp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer httpResp.Body.Close()

	var msg map[string]any
	json.NewDecoder(httpResp.Body).Decode(&msg) //nolint:errcheck
	content := msg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(content))
	}
	if content[0].(map[string]any)["type"] != "text" {
		t.Errorf("first block should be text")
	}
	if content[1].(map[string]any)["type"] != "tool_use" {
		t.Errorf("second block should be tool_use")
	}
}

// TestMockAnthropicServer_StreamingMixedContent verifies SSE events for a
// response with both text and tool_use blocks.
func TestMockAnthropicServer_StreamingMixedContent(t *testing.T) {
	inputJSON, _ := json.Marshal(map[string]any{"query": "test"})
	resp := MockAnthropicResponse{
		Content: []MockContentBlock{
			{Type: "text", Text: "Searching..."},
			{Type: "tool_use", ID: "call_1", Name: "search", Input: inputJSON},
		},
		StopReason: "tool_use",
		Usage:      MockUsage{InputTokens: 15, OutputTokens: 10},
	}
	srv := NewMockAnthropicServer(t, resp)

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"search"}]}`
	httpResp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer httpResp.Body.Close()

	events := parseSSEEvents(t, httpResp.Body)

	// Expected: message_start, (text: start/delta/stop), (tool_use: start/delta/stop),
	// message_delta, message_stop = 1 + 3 + 3 + 2 = 9
	if len(events) != 9 {
		t.Fatalf("expected 9 events, got %d: %v", len(events), eventNames(events))
	}

	expectedTypes := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_stop", // text
		"content_block_start", "content_block_delta", "content_block_stop", // tool_use
		"message_delta",
		"message_stop",
	}
	for i, want := range expectedTypes {
		if events[i].event != want {
			t.Errorf("event[%d]: expected %s, got %s", i, want, events[i].event)
		}
	}
}

// TestMockAnthropicServer_AddResponse verifies that AddResponse appends to
// the queue dynamically.
func TestMockAnthropicServer_AddResponse(t *testing.T) {
	srv := NewMockAnthropicServer(t)
	srv.AddResponse(MockTextResponse("added later"))

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestMockAnthropicServer_InvalidJSON verifies that malformed JSON returns 400.
func TestMockAnthropicServer_InvalidJSON(t *testing.T) {
	srv := NewMockAnthropicServer(t, MockTextResponse("unused"))

	resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

// --- SSE parsing helpers ---

type sseEvent struct {
	event string
	data  string
}

// parseSSEEvents reads the full response body and returns parsed SSE events.
func parseSSEEvents(t *testing.T, body io.Reader) []sseEvent {
	t.Helper()
	var events []sseEvent

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading SSE body: %v", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	var currentEvent string
	var currentData string

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			currentData = strings.TrimPrefix(line, "data: ")
		case line == "":
			// Blank line terminates an event.
			if currentEvent != "" {
				events = append(events, sseEvent{event: currentEvent, data: currentData})
				currentEvent = ""
				currentData = ""
			}
		}
	}
	// Catch final event if body doesn't end with blank line.
	if currentEvent != "" {
		events = append(events, sseEvent{event: currentEvent, data: currentData})
	}

	return events
}

// eventNames returns the event types for debugging.
func eventNames(events []sseEvent) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.event
	}
	return names
}

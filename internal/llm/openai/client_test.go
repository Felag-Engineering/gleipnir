package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openai/openai-go/option"
	"github.com/rapp992/gleipnir/internal/llm"
)

// newTestClient builds a Client pointed at the given httptest.Server.
func newTestClient(srv *httptest.Server) *Client {
	return NewClient("test-key",
		option.WithHTTPClient(srv.Client()),
		option.WithBaseURL(srv.URL),
	)
}

// loadFixture reads a file from the testdata directory and returns its bytes.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("loadFixture: %v", err)
	}
	return data
}

func TestCreateMessage_HappyPath(t *testing.T) {
	fixture := loadFixture(t, "response_text_only.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected Authorization header: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gpt-4o",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "Hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Text) == 0 {
		t.Fatal("expected at least one text block")
	}
	if got := resp.Text[0].Text; got != "Hello from OpenAI." {
		t.Errorf("text = %q; want %q", got, "Hello from OpenAI.")
	}
	if resp.StopReason != llm.StopReasonEndTurn {
		t.Errorf("StopReason = %v; want StopReasonEndTurn", resp.StopReason)
	}
}

func TestCreateMessage_ToolCalls(t *testing.T) {
	fixture := loadFixture(t, "response_with_tool_calls.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gpt-4o",
		Tools: []llm.ToolDefinition{{Name: "get_weather", Description: "get weather"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) == 0 {
		t.Fatal("expected at least one tool call")
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "get_weather" {
		t.Errorf("tool name = %q; want get_weather", tc.Name)
	}
	if tc.ID != "call_abc123" {
		t.Errorf("tool call_id = %q; want call_abc123", tc.ID)
	}
	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("StopReason = %v; want StopReasonToolUse", resp.StopReason)
	}
}

func TestCreateMessage_ReasoningTokens(t *testing.T) {
	fixture := loadFixture(t, "response_with_reasoning.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{Model: "o3-mini"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.ThinkingTokens != 15 {
		t.Errorf("ThinkingTokens = %d; want 15", resp.Usage.ThinkingTokens)
	}
	if len(resp.Thinking) == 0 {
		t.Fatal("expected at least one thinking block")
	}
	if resp.Thinking[0].Text != "I need to compute the answer." {
		t.Errorf("thinking text = %q", resp.Thinking[0].Text)
	}
}

func TestCreateMessage_ErrorStatus(t *testing.T) {
	fixture := loadFixture(t, "response_error_401.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(fixture)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if !containsString(err.Error(), "401") {
		t.Errorf("error %q does not contain 401", err.Error())
	}
}

func TestCreateMessage_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := newTestClient(srv)
	_, err := client.CreateMessage(ctx, llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestCreateMessage_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := newTestClient(srv)
	srv.Close()

	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error on closed server")
	}
}

func TestStreamMessage_HappyPath(t *testing.T) {
	// The Responses API streaming returns SSE events. Each data line contains a
	// JSON object whose "type" field discriminates the event kind.
	stream := buildTextSSE()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(stream)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	ch, err := client.StreamMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var accumulated string
	sawStop := false
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		if chunk.Text != nil {
			accumulated += *chunk.Text
		}
		if chunk.StopReason != nil {
			sawStop = true
		}
	}

	if accumulated != "Hello world" {
		t.Errorf("accumulated text = %q; want %q", accumulated, "Hello world")
	}
	if !sawStop {
		t.Error("expected a stop chunk, got none")
	}
}

func TestStreamMessage_HTTPErrorBeforeStream(t *testing.T) {
	fixture := loadFixture(t, "response_error_401.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(fixture)
	}))
	defer srv.Close()

	// SDK's NewStreaming defers the error handling to the stream iteration.
	// We drain the channel and expect an error chunk.
	client := newTestClient(srv)
	ch, err := client.StreamMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	// The SDK may return the error synchronously or on first Next() call.
	if err != nil {
		// synchronous error path — acceptable
		if !containsString(err.Error(), "401") {
			t.Errorf("error %q does not contain 401", err.Error())
		}
		return
	}
	// Async error path — drain channel and look for an error chunk.
	var sawErr bool
	for chunk := range ch {
		if chunk.Err != nil {
			sawErr = true
			if !containsString(chunk.Err.Error(), "401") {
				t.Errorf("error chunk %q does not contain 401", chunk.Err.Error())
			}
		}
	}
	if !sawErr {
		t.Error("expected an error chunk for 401, got none")
	}
}

// buildTextSSE constructs a minimal Responses API SSE stream that delivers
// "Hello" + " world" text deltas and a completed event.
func buildTextSSE() []byte {
	events := []string{
		`data: {"type":"response.output_text.delta","item_id":"msg_001","output_index":0,"content_index":0,"delta":"Hello","sequence_number":1}`,
		``,
		`data: {"type":"response.output_text.delta","item_id":"msg_001","output_index":0,"content_index":0,"delta":" world","sequence_number":2}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_001","object":"response","created_at":1700000000,"model":"gpt-4o","status":"completed","output":[{"id":"msg_001","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello world","annotations":[]}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}},"sequence_number":3}`,
		``,
		`data: [DONE]`,
		``,
	}
	var out []byte
	for _, e := range events {
		out = append(out, []byte(e+"\n")...)
	}
	return out
}

// TestCuratedModelIsReasoning verifies that the IsReasoning flag on each
// curated model entry is plumbed correctly through curatedModelIsReasoning,
// and that unknown models return false.
func TestCuratedModelIsReasoning(t *testing.T) {
	// Verify every curated model entry is reflected correctly.
	for _, m := range curatedModels {
		t.Run(m.Name, func(t *testing.T) {
			if got := curatedModelIsReasoning(m.Name); got != m.IsReasoning {
				t.Errorf("curatedModelIsReasoning(%q) = %v; want %v (from ModelInfo.IsReasoning)", m.Name, got, m.IsReasoning)
			}
		})
	}

	// Unknown models must return false — they should not receive the include.
	unknowns := []string{"gpt-4o", "gpt-4o-mini", "not-a-real-model"}
	for _, name := range unknowns {
		t.Run("unknown/"+name, func(t *testing.T) {
			if curatedModelIsReasoning(name) {
				t.Errorf("curatedModelIsReasoning(%q) = true; want false for unknown model", name)
			}
		})
	}
}

// containsString is a helper that avoids importing strings in test bodies.
func containsString(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/llm"
)

// newTestClient builds a Client pointed at the given httptest.Server.
func newTestClient(srv *httptest.Server) *Client {
	return NewClient(srv.URL, "test-key",
		WithHTTPClient(srv.Client()),
		WithTimeout(5*time.Second),
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
	fixture := loadFixture(t, "chat_response_text_only.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
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

func TestCreateMessage_SendsToolsInBody(t *testing.T) {
	fixture := loadFixture(t, "chat_response_text_only.json")

	var gotBody chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gpt-4o",
		Tools: []llm.ToolDefinition{
			{
				Name:        "echo",
				Description: "Echoes its input",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotBody.Tools) == 0 {
		t.Fatal("expected tools in request body")
	}
	if got := gotBody.Tools[0].Function.Name; got != "echo" {
		t.Errorf("tool name = %q; want %q", got, "echo")
	}
}

func TestCreateMessage_ErrorStatuses(t *testing.T) {
	cases := []struct {
		status  int
		fixture string
	}{
		{401, "chat_response_error_401.json"},
		{429, "chat_response_error_429.json"},
		{500, "chat_response_error_500.json"},
	}

	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			fixture := loadFixture(t, tc.fixture)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				w.Write(fixture)
			}))
			defer srv.Close()

			client := newTestClient(srv)
			_, err := client.CreateMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			want := fmt.Sprintf("%d", tc.status)
			if !containsString(err.Error(), want) {
				t.Errorf("error %q does not contain status %s", err.Error(), want)
			}
		})
	}
}

func TestCreateMessage_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := newTestClient(srv)
	// Close the server before the request — the dial will fail.
	srv.Close()

	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error on closed server")
	}
}

func TestCreateMessage_ContextCancellation(t *testing.T) {
	// The handler sleeps long enough that the pre-cancelled context fires first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the request is made

	client := newTestClient(srv)
	_, err := client.CreateMessage(ctx, llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestListModels_HappyPathAndCache(t *testing.T) {
	fixture := loadFixture(t, "models_response.json")
	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := newTestClient(srv)

	// First call: fetches from server.
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 3 {
		t.Errorf("len(models) = %d; want 3", len(models))
	}
	if hits.Load() != 1 {
		t.Errorf("server hits after first call = %d; want 1", hits.Load())
	}

	// Second call: served from cache, no additional server hit.
	_, err = client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("server hits after second call = %d; want still 1", hits.Load())
	}

	// After invalidation, the next call must hit the server again.
	client.InvalidateModelCache()
	_, err = client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error after invalidation: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("server hits after invalidation + third call = %d; want 2", hits.Load())
	}
}

func TestListModels_404IsEmptySlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected empty slice, got %d models", len(models))
	}
}

func TestValidateModelName_AgainstUnknownBackendAccepts(t *testing.T) {
	// A 404 from /models means the backend doesn't expose the endpoint.
	// Any non-empty model name must be accepted in that case (ADR-032).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	if err := client.ValidateModelName(context.Background(), "any-model-name"); err != nil {
		t.Errorf("expected nil for unknown-backend, got: %v", err)
	}
}

func TestValidateModelName_KnownAndUnknown(t *testing.T) {
	fixture := loadFixture(t, "models_response.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := newTestClient(srv)

	// "gpt-4o" is in models_response.json — should be accepted.
	if err := client.ValidateModelName(context.Background(), "gpt-4o"); err != nil {
		t.Errorf("expected nil for known model, got: %v", err)
	}

	// "does-not-exist" is not in the fixture — should be rejected.
	if err := client.ValidateModelName(context.Background(), "does-not-exist"); err == nil {
		t.Error("expected error for unknown model, got nil")
	}
}

func TestStreamMessage_HappyPath(t *testing.T) {
	fixture := loadFixture(t, "stream_chunks_text.txt")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var wireReq chatRequest
		if err := json.NewDecoder(r.Body).Decode(&wireReq); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if !wireReq.Stream {
			t.Error("expected stream=true in request body")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(fixture)
	}))
	defer srv.Close()

	ch, err := newTestClient(srv).StreamMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
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
	fixture := loadFixture(t, "chat_response_error_401.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(fixture)
	}))
	defer srv.Close()

	ch, err := newTestClient(srv).StreamMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
	if ch != nil {
		t.Error("expected nil channel on error")
	}
	if !containsString(err.Error(), "401") {
		t.Errorf("error %q does not contain status 401", err.Error())
	}
}

// containsString is a helper that avoids importing strings in test bodies.
func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

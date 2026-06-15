package openaicompat

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/llm"
)

// countingRoundTripper always fails with a connection-level error and records
// how many times it was invoked, so a test can assert the retry loop re-dialed.
type countingRoundTripper struct{ n *atomic.Int32 }

func (c countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
}

// TestCreateMessage_RetriesOnConnectionError proves the connection-error path:
// a dial failure (no HTTP response) is retried with backoff up to the attempt
// budget, then surfaces the network error.
func TestCreateMessage_RetriesOnConnectionError(t *testing.T) {
	orig := llm.DefaultRetryConfig
	llm.SetDefaultRetryConfig(llm.RetryConfig{
		MaxAttempts:    3,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	})
	t.Cleanup(func() { llm.DefaultRetryConfig = orig })

	var dials atomic.Int32
	client := NewClient("http://example.invalid", "test-key",
		WithHTTPClient(&http.Client{Transport: countingRoundTripper{n: &dials}}),
	)

	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model:   "gpt-4o",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected a connection error after exhausting retries")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) {
		t.Fatalf("expected a net.Error, got %v", err)
	}
	if got := dials.Load(); got != 3 {
		t.Fatalf("expected 3 dials (MaxAttempts), got %d", got)
	}
}

// TestCreateMessage_DoesNotRetryCancelledContext confirms context cancellation
// is terminal — the connection-error retry must not fight cancellation.
func TestCreateMessage_DoesNotRetryCancelledContext(t *testing.T) {
	orig := llm.DefaultRetryConfig
	llm.SetDefaultRetryConfig(llm.RetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	t.Cleanup(func() { llm.DefaultRetryConfig = orig })

	var dials atomic.Int32
	client := NewClient("http://example.invalid", "test-key",
		WithHTTPClient(&http.Client{Transport: countingRoundTripper{n: &dials}}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.CreateMessage(ctx, llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected an error for cancelled context")
	}
	// doRequest returns ctx.Err() before any dial when the context is already
	// cancelled, so there should be at most one attempt and no retry loop.
	if got := dials.Load(); got > 1 {
		t.Fatalf("expected no retries on cancelled context, got %d dials", got)
	}
}

// TestCreateMessage_RetriesOn429 proves the end-to-end retry path: a transient
// 429 with a Retry-After header is retried (re-issuing the request with a fresh
// body reader) and the eventual 200 succeeds. It also confirms the classifier
// + DoWithRetry wiring honors the configured attempt budget.
func TestCreateMessage_RetriesOn429(t *testing.T) {
	// Shrink backoff so the test does not actually wait seconds; restore after.
	orig := llm.DefaultRetryConfig
	llm.SetDefaultRetryConfig(llm.RetryConfig{
		MaxAttempts:    4,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	})
	t.Cleanup(func() { llm.DefaultRetryConfig = orig })

	fixture := loadFixture(t, "chat_response_text_only.json")
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 { // fail the first two attempts, succeed on the third
			w.Header().Set("Retry-After", "0") // 0 → loop uses computed backoff
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
			return
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
		t.Fatalf("expected success after retries, got %v", err)
	}
	if len(resp.Text) == 0 {
		t.Fatal("expected a text block in the eventual success response")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 attempts (2 retries), got %d", got)
	}
}

// TestStreamMessage_RetriesOnTransientEstablishment proves the streaming path
// retries stream ESTABLISHMENT the same way the sync Call path does: two pre-stream
// 503s are retried and the eventual SSE body streams normally. Mirrors the sync
// TestCreateMessage_RetriesOn429 but exercises StreamMessage.
func TestStreamMessage_RetriesOnTransientEstablishment(t *testing.T) {
	orig := llm.DefaultRetryConfig
	llm.SetDefaultRetryConfig(llm.RetryConfig{
		MaxAttempts:    4,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	})
	t.Cleanup(func() { llm.DefaultRetryConfig = orig })

	streamBody := loadFixture(t, "stream_chunks_text.txt")
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 { // fail the first two attempts with a transient 503
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"overloaded"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(streamBody)
	}))
	defer srv.Close()

	ch, err := newTestClient(srv).StreamMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("expected stream to establish after retries, got %v", err)
	}

	var accumulated string
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		if chunk.Text != nil {
			accumulated += *chunk.Text
		}
	}
	if accumulated != "Hello world" {
		t.Errorf("accumulated text = %q; want %q", accumulated, "Hello world")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 establishment attempts (2 retries), got %d", got)
	}
}

// TestStreamMessage_DoesNotRetryOn4xx confirms a deterministic client error on
// stream establishment (here 400) is terminal even with a generous attempt budget
// — the classifier, not the budget, must stop the retry.
func TestStreamMessage_DoesNotRetryOn4xx(t *testing.T) {
	orig := llm.DefaultRetryConfig
	llm.SetDefaultRetryConfig(llm.RetryConfig{MaxAttempts: 4, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	t.Cleanup(func() { llm.DefaultRetryConfig = orig })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer srv.Close()

	ch, err := newTestClient(srv).StreamMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for 400 establishment, got nil")
	}
	if ch != nil {
		t.Error("expected nil channel on establishment error")
	}
	var httpErr *llm.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected a 400 HTTPError, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry on 4xx), got %d", got)
	}
}

// TestCreateMessage_ExhaustsRetriesOn429 confirms a persistent 429 fails after
// the attempt budget is spent and surfaces the rate-limit error.
func TestCreateMessage_ExhaustsRetriesOn429(t *testing.T) {
	orig := llm.DefaultRetryConfig
	llm.SetDefaultRetryConfig(llm.RetryConfig{
		MaxAttempts:    3,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	})
	t.Cleanup(func() { llm.DefaultRetryConfig = orig })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	client := newTestClient(srv)
	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gpt-4o",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "Hi"}}},
		},
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	var httpErr *llm.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected a 429 HTTPError, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 attempts (MaxAttempts), got %d", got)
	}
}

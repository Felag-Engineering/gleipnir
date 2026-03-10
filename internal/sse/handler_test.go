package sse

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// drainLines reads lines from the response body until either n non-empty lines
// are accumulated or the deadline elapses. Returns the collected lines.
func drainLines(t *testing.T, resp *http.Response, n int, deadline time.Duration) []string {
	t.Helper()
	ch := make(chan string)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" {
				ch <- line
			}
		}
		close(ch)
	}()

	var lines []string
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for len(lines) < n {
		select {
		case line, ok := <-ch:
			if !ok {
				return lines
			}
			lines = append(lines, line)
		case <-timer.C:
			t.Logf("drainLines: timed out after collecting %d/%d lines", len(lines), n)
			return lines
		}
	}
	return lines
}

func TestHandler_Headers(t *testing.T) {
	b := NewBroadcaster()
	h := NewHandler(b)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, req)
	}()

	// Cancel the context to terminate ServeHTTP, then wait.
	cancel()
	<-done

	resp := w.Result()
	check := func(header, want string) {
		t.Helper()
		if got := resp.Header.Get(header); got != want {
			t.Errorf("header %q = %q, want %q", header, got, want)
		}
	}
	check("Content-Type", "text/event-stream")
	check("Cache-Control", "no-cache")
	check("Connection", "keep-alive")
	check("X-Accel-Buffering", "no")
}

func TestHandler_StreamsEvents(t *testing.T) {
	b := NewBroadcaster()
	h := NewHandler(b)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)

	// httptest.NewRecorder does not implement http.Flusher, so we need a real
	// streaming test server.
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/events") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()
	_ = req // not used with test server path

	b.Publish("run.status_changed", rawJSON(`{"run_id":"r1","status":"running"}`))
	b.Publish("run.step_added", rawJSON(`{"run_id":"r1","step_id":"s1"}`))

	lines := drainLines(t, resp, 6, 3*time.Second)
	if len(lines) < 6 {
		t.Fatalf("got %d lines, want at least 6; lines: %v", len(lines), lines)
	}

	// Lines for the first event: id, event, data.
	if !strings.HasPrefix(lines[0], "id: 1") {
		t.Errorf("lines[0] = %q, want prefix %q", lines[0], "id: 1")
	}
	if !strings.HasPrefix(lines[1], "event: run.status_changed") {
		t.Errorf("lines[1] = %q, want prefix %q", lines[1], "event: run.status_changed")
	}
	if !strings.HasPrefix(lines[2], "data: ") {
		t.Errorf("lines[2] = %q, want prefix %q", lines[2], "data: ")
	}

	// Lines for the second event.
	if !strings.HasPrefix(lines[3], "id: 2") {
		t.Errorf("lines[3] = %q, want prefix %q", lines[3], "id: 2")
	}
	if !strings.HasPrefix(lines[4], "event: run.step_added") {
		t.Errorf("lines[4] = %q, want prefix %q", lines[4], "event: run.step_added")
	}
}

func TestHandler_LastEventID_Replay(t *testing.T) {
	b := NewBroadcaster()
	h := NewHandler(b)

	// Pre-publish 3 events into the ring buffer before any client connects.
	b.Publish("test", rawJSON(`{"n":1}`))
	b.Publish("test", rawJSON(`{"n":2}`))
	b.Publish("test", rawJSON(`{"n":3}`))

	srv := httptest.NewServer(h)
	defer srv.Close()

	// Reconnect with Last-Event-ID: 1 — should only replay events 2 and 3.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Last-Event-ID", "1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()

	// Expect 2 events × 3 lines each = 6 lines.
	lines := drainLines(t, resp, 6, 3*time.Second)
	if len(lines) < 6 {
		t.Fatalf("got %d lines, want at least 6 (2 replayed events); lines: %v", len(lines), lines)
	}

	// First replayed event must be ID 2.
	if want := "id: 2"; !strings.HasPrefix(lines[0], want) {
		t.Errorf("first replayed event: lines[0] = %q, want prefix %q", lines[0], want)
	}
	// Second replayed event must be ID 3.
	if want := "id: 3"; !strings.HasPrefix(lines[3], want) {
		t.Errorf("second replayed event: lines[3] = %q, want prefix %q", lines[3], want)
	}
}

func TestHandler_ContextCancellation(t *testing.T) {
	b := NewBroadcaster()
	h := NewHandler(b)

	ctx, cancel := context.WithCancel(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, req)
	}()

	cancel()

	select {
	case <-done:
		// ServeHTTP returned cleanly after context cancellation.
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return after context cancellation")
	}
}

func TestHandler_EventFormat(t *testing.T) {
	b := NewBroadcaster()
	h := NewHandler(b)

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/events") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()

	b.Publish("my.event", rawJSON(`{"key":"value"}`))

	lines := drainLines(t, resp, 3, 3*time.Second)
	if len(lines) < 3 {
		t.Fatalf("got %d lines, want 3; lines: %v", len(lines), lines)
	}

	wantLines := []string{
		fmt.Sprintf("id: %d", 1),
		"event: my.event",
		`data: {"key":"value"}`,
	}
	for i, want := range wantLines {
		if !strings.HasPrefix(lines[i], want) {
			t.Errorf("lines[%d] = %q, want prefix %q", i, lines[i], want)
		}
	}
}

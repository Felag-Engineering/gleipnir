//go:build unix

package process_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/plugin/process"
)

// capturingHandler records all slog records so tests can inspect what was logged.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()
	return nil
}

func (h *capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Return a new handler that inherits the existing records slice so child
	// loggers created via logger.With(...) still write to the same slice.
	return &capturingHandlerWithAttrs{parent: h, attrs: attrs}
}

func (h *capturingHandler) WithGroup(_ string) slog.Handler { return h }

func (h *capturingHandler) allMessages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.records))
	for i, r := range h.records {
		out[i] = r.Message
	}
	return out
}

// capturingHandlerWithAttrs wraps capturingHandler with extra attributes
// pre-applied so logger.With(...) works correctly in tests.
type capturingHandlerWithAttrs struct {
	parent *capturingHandler
	attrs  []slog.Attr
}

func (h *capturingHandlerWithAttrs) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingHandlerWithAttrs) Handle(ctx context.Context, r slog.Record) error {
	r.AddAttrs(h.attrs...)
	return h.parent.Handle(ctx, r)
}

func (h *capturingHandlerWithAttrs) WithAttrs(attrs []slog.Attr) slog.Handler {
	all := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	all = append(all, h.attrs...)
	all = append(all, attrs...)
	return &capturingHandlerWithAttrs{parent: h.parent, attrs: all}
}

func (h *capturingHandlerWithAttrs) WithGroup(_ string) slog.Handler { return h }

// TestPipeLines_LevelsAndLabels verifies that PipeLines logs each written line
// as a separate slog record and signals the done channel when the writer is
// closed.
func TestPipeLines_LevelsAndLabels(t *testing.T) {
	handler := &capturingHandler{}
	logger := slog.New(handler)

	w, done := process.PipeLines(logger, slog.LevelWarn, "stderr")

	lines := []string{"line one", "line two", "line three"}
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			t.Fatalf("write line: %v", err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	// Wait for the goroutine to drain with a reasonable timeout.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done channel did not close within 5s")
	}

	msgs := handler.allMessages()
	if len(msgs) < len(lines) {
		t.Fatalf("want at least %d log records, got %d: %v", len(lines), len(msgs), msgs)
	}

	for _, want := range lines {
		found := false
		for _, got := range msgs {
			if strings.Contains(got, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected log record containing %q, not found in %v", want, msgs)
		}
	}
}

// TestPipeLines_DoneOnClose verifies the done channel closes when the writer
// is closed, even when no data was written.
func TestPipeLines_DoneOnClose(t *testing.T) {
	handler := &capturingHandler{}
	logger := slog.New(handler)

	w, done := process.PipeLines(logger, slog.LevelInfo, "stderr")
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("done channel did not close after writer was closed")
	}
}

// TestPipeLines_StreamAttribute verifies that the "stream" attribute passed to
// PipeLines appears in every logged record.
func TestPipeLines_StreamAttribute(t *testing.T) {
	handler := &capturingHandler{}
	logger := slog.New(handler)

	w, done := process.PipeLines(logger, slog.LevelWarn, "stderr")
	fmt.Fprintln(w, "test message")
	w.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("done channel did not close")
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.records) == 0 {
		t.Fatal("expected at least one log record")
	}
	r := handler.records[0]
	var foundStream bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "stream" && a.Value.String() == "stderr" {
			foundStream = true
		}
		return true
	})
	if !foundStream {
		t.Error("expected stream=stderr attribute in log record")
	}
}

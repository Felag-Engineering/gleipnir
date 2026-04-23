package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

func TestAuditWriter_ConcurrentEnqueue(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())

	const goroutines = 10
	const stepsEach = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range stepsEach {
				if err := w.Write(context.Background(), Step{
					RunID:   "r1",
					Type:    model.StepTypeThought,
					Content: map[string]string{"msg": "hello"},
				}); err != nil {
					t.Errorf("Write: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify row count
	count, err := s.CountRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("CountRunSteps: %v", err)
	}
	const want = goroutines * stepsEach
	if count != want {
		t.Errorf("step count = %d, want %d", count, want)
	}

	// Verify step_numbers are unique and span 0..999
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "r1", After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	seen := make(map[int64]bool, len(steps))
	for _, step := range steps {
		if seen[step.StepNumber] {
			t.Errorf("duplicate step_number %d", step.StepNumber)
		}
		seen[step.StepNumber] = true
		if step.StepNumber < 0 || step.StepNumber >= want {
			t.Errorf("step_number %d out of range [0, %d)", step.StepNumber, want)
		}
	}
}

func TestAuditWriter_TokenCostAccumulation(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())

	steps := []Step{
		{RunID: "r1", Type: model.StepTypeThought, Content: "a", TokenCost: 10},
		{RunID: "r1", Type: model.StepTypeThought, Content: "b", TokenCost: 25},
		{RunID: "r1", Type: model.StepTypeThought, Content: "c", TokenCost: 0},
		{RunID: "r1", Type: model.StepTypeThought, Content: "d", TokenCost: 5},
	}
	wantTotal := int64(10 + 25 + 0 + 5)

	for _, step := range steps {
		if err := w.Write(context.Background(), step); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	run, err := s.GetRun(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.TokenCost != wantTotal {
		t.Errorf("token_cost = %d, want %d", run.TokenCost, wantTotal)
	}
}

func TestAuditWriter_StopDrainsQueue(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())

	const count = 50
	for i := range count {
		if err := w.Write(context.Background(), Step{
			RunID:   "r1",
			Type:    model.StepTypeThought,
			Content: i,
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	// Close must not return until all enqueued writes have landed.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := s.CountRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("CountRunSteps: %v", err)
	}
	if got != count {
		t.Errorf("after Close: %d rows, want %d", got, count)
	}
}

func TestAuditWriter_ContextCancellation(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	// Use a queue depth of 0 to force the enqueue to block immediately, so
	// the cancel fires while Write is waiting to send into the queue.
	w := NewAuditWriter(s.Queries(), WithQueueDepth(0))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	err := w.Write(ctx, Step{
		RunID:   "r1",
		Type:    model.StepTypeThought,
		Content: "should not land",
	})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}

	// No deadlock: Close must return promptly.
	done := make(chan struct{})
	go func() {
		w.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked after context cancellation")
	}
}

func TestAuditWriter_MultipleRuns(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)
	testutil.InsertRun(t, s, "r2", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())

	// Interleave writes across two runs.
	pairs := []struct{ runID string }{
		{"r1"}, {"r2"}, {"r1"}, {"r2"}, {"r1"},
	}
	for _, p := range pairs {
		if err := w.Write(context.Background(), Step{
			RunID:   p.runID,
			Type:    model.StepTypeThought,
			Content: "x",
		}); err != nil {
			t.Fatalf("Write(%s): %v", p.runID, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	want := map[string]int64{"r1": 3, "r2": 2}
	for runID, wantCount := range want {
		got, err := s.CountRunSteps(context.Background(), runID)
		if err != nil {
			t.Fatalf("CountRunSteps(%s): %v", runID, err)
		}
		if got != wantCount {
			t.Errorf("run %s: %d steps, want %d", runID, got, wantCount)
		}

		// step_numbers must be 0..N-1 with no gaps for each run.
		steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: listAll})
		if err != nil {
			t.Fatalf("ListRunSteps(%s): %v", runID, err)
		}
		for i, step := range steps {
			if step.StepNumber != int64(i) {
				t.Errorf("run %s step[%d]: step_number = %d, want %d",
					runID, i, step.StepNumber, i)
			}
		}
	}
}

func TestAuditWriter_WithPublisher_EmitsStepAdded(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	w := NewAuditWriter(s.Queries(), WithPublisher(pub))

	if err := w.Write(context.Background(), Step{
		RunID:   "r1",
		Type:    model.StepTypeThought,
		Content: "hello",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := pub.all()
	if len(events) != 1 {
		t.Fatalf("got %d published events, want 1", len(events))
	}
	if events[0].eventType != "run.step_added" {
		t.Errorf("event type = %q, want %q", events[0].eventType, "run.step_added")
	}

	var payload map[string]any
	if err := json.Unmarshal(events[0].data, &payload); err != nil {
		t.Fatalf("unmarshal event data: %v", err)
	}
	if payload["run_id"] != "r1" {
		t.Errorf("run_id = %v, want %q", payload["run_id"], "r1")
	}
	if payload["step_number"] == nil {
		t.Error("step_number is nil, want non-nil")
	}
	if payload["type"] == nil {
		t.Error("type is nil, want non-nil")
	}
}

func TestAuditWriter_NilPublisherIsSafe(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	// No publisher — should not panic.
	w := NewAuditWriter(s.Queries())
	if err := w.Write(context.Background(), Step{
		RunID:   "r1",
		Type:    model.StepTypeThought,
		Content: "hello",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestAuditWriter_CloseReturnsDrainError(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())

	// "nonexistent-run" has no matching row, so CreateRunStep will fail with a
	// foreign key constraint error. Write must propagate that error to the caller.
	writeErr := w.Write(context.Background(), Step{
		RunID:   "nonexistent-run",
		Type:    model.StepTypeThought,
		Content: "should fail",
	})
	if writeErr == nil {
		t.Fatal("Write: expected non-nil error for unknown run ID, got nil")
	}

	// Close must also surface the accumulated drain error.
	closeErr := w.Close()
	if closeErr == nil {
		t.Fatal("Close: expected non-nil error after drain failure, got nil")
	}
}

// captureHandler is a slog.Handler that records every log record for assertion.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(name string) slog.Handler       { return h }

func (h *captureHandler) all() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

func TestLogAuditError_SuccessfulWrite_NoLog(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	h := &captureHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	w := NewAuditWriter(s.Queries())
	logAuditError(context.Background(), w, Step{
		RunID:   "r1",
		Type:    model.StepTypeError,
		Content: model.ErrorStepContent{Message: "test", Code: "test_code"},
	})
	w.Close()

	if records := h.all(); len(records) != 0 {
		t.Errorf("expected no log records on successful write, got %d", len(records))
	}
}

func TestLogAuditError_FailedWrite_LogsWarn(t *testing.T) {
	s := testutil.NewTestStore(t)
	// Do NOT insert a run — the write will fail with a foreign key constraint error.

	h := &captureHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	w := NewAuditWriter(s.Queries())
	logAuditError(context.Background(), w, Step{
		RunID:   "nonexistent-run",
		Type:    model.StepTypeError,
		Content: model.ErrorStepContent{Message: "test", Code: "test_code"},
	})
	w.Close()

	records := h.all()
	if len(records) != 1 {
		t.Fatalf("expected 1 log record, got %d", len(records))
	}
	r := records[0]
	if r.Level != slog.LevelWarn {
		t.Errorf("log level = %v, want WARN", r.Level)
	}
	if r.Message != "audit write failed on error path" {
		t.Errorf("log message = %q, want %q", r.Message, "audit write failed on error path")
	}

	// Verify expected attributes are present.
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	if attrs["step_type"] == nil {
		t.Error("expected step_type attribute in log record")
	}
	if attrs["run_id"] == nil {
		t.Error("expected run_id attribute in log record")
	}
	if attrs["err"] == nil {
		t.Error("expected err attribute in log record")
	}
}

func BenchmarkAuditWriter_SequentialEnqueue(b *testing.B) {
	s := testutil.NewTestStore(b)
	testutil.InsertPolicy(b, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(b, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := w.Write(context.Background(), Step{
			RunID:   "r1",
			Type:    model.StepTypeThought,
			Content: i,
		}); err != nil {
			b.Fatalf("Write: %v", err)
		}
	}

	b.StopTimer()
	w.Close()
}

func TestAuditWriter_PerformanceBaseline(t *testing.T) {
	// Asserts that 1000 sequential enqueues complete within 10s.
	// Threshold is generous to avoid flaking on slow CI runners.
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())

	start := time.Now()
	for i := range 1000 {
		if err := w.Write(context.Background(), Step{
			RunID:   "r1",
			Type:    model.StepTypeThought,
			Content: i,
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Errorf("1000 sequential enqueues took %v, want < 10s", elapsed)
	}
}

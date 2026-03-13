package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

func TestAuditWriter_ConcurrentEnqueue(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries)

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

	// Verify step_numbers are unique and span 1..1000
	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	seen := make(map[int64]bool, len(steps))
	for _, step := range steps {
		if seen[step.StepNumber] {
			t.Errorf("duplicate step_number %d", step.StepNumber)
		}
		seen[step.StepNumber] = true
		if step.StepNumber < 1 || step.StepNumber > want {
			t.Errorf("step_number %d out of range [1, %d]", step.StepNumber, want)
		}
	}
}

func TestAuditWriter_TokenCostAccumulation(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries)

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

	w := NewAuditWriter(s.Queries)

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
	w := NewAuditWriter(s.Queries, WithQueueDepth(0))

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

	w := NewAuditWriter(s.Queries)

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

		// step_numbers must be 1..N with no gaps for each run.
		steps, err := s.ListRunSteps(context.Background(), runID)
		if err != nil {
			t.Fatalf("ListRunSteps(%s): %v", runID, err)
		}
		for i, step := range steps {
			if step.StepNumber != int64(i+1) {
				t.Errorf("run %s step[%d]: step_number = %d, want %d",
					runID, i, step.StepNumber, i+1)
			}
		}
	}
}

func TestAuditWriter_WithPublisher_EmitsStepAdded(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	w := NewAuditWriter(s.Queries, WithPublisher(pub))

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
	w := NewAuditWriter(s.Queries)
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

func BenchmarkAuditWriter_SequentialEnqueue(b *testing.B) {
	s := testutil.NewTestStore(b)
	testutil.InsertPolicy(b, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(b, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries)
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

func TestAuditWriter_BenchmarkBaseline(t *testing.T) {
	// Asserts that 1000 sequential enqueues complete within 500ms.
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries)

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

	if elapsed > 5*time.Second {
		t.Errorf("1000 sequential enqueues took %v, want < 5s", elapsed)
	}
}

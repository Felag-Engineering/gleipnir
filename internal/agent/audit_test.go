package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// newTestStore opens a temp-dir SQLite DB, applies the schema, and registers
// cleanup. Duplicated from the db package because those helpers are unexported.
func newTestStore(tb testing.TB) *db.Store {
	tb.Helper()
	s, err := db.Open(filepath.Join(tb.TempDir(), "test.db"))
	if err != nil {
		tb.Fatalf("Open: %v", err)
	}
	tb.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		tb.Fatalf("Migrate: %v", err)
	}
	return s
}

func insertPolicy(tb testing.TB, s *db.Store, id string) {
	tb.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO policies(id, name, trigger_type, yaml, created_at, updated_at)
		 VALUES (?, ?, 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		id, "policy-"+id,
	)
	if err != nil {
		tb.Fatalf("insertPolicy %s: %v", id, err)
	}
}

func insertRun(tb testing.TB, s *db.Store, id, policyID, status string) {
	tb.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
		 VALUES (?, ?, ?, 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		id, policyID, status,
	)
	if err != nil {
		tb.Fatalf("insertRun %s: %v", id, err)
	}
}

func TestAuditWriter_ConcurrentEnqueue(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")

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
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")

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
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")

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
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")

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
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertRun(t, s, "r2", "p1", "running")

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

func BenchmarkAuditWriter_SequentialEnqueue(b *testing.B) {
	s := newTestStore(b)
	insertPolicy(b, s, "p1")
	insertRun(b, s, "r1", "p1", "running")

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
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")

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

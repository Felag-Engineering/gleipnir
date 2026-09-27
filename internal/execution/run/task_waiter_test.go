package run_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
)

// fakeTaskCanceler records every rowID passed to Cancel.
type fakeTaskCanceler struct {
	mu       sync.Mutex
	canceled []string
	err      error
}

func (f *fakeTaskCanceler) Cancel(_ context.Context, rowID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, rowID)
	return f.err
}

func (f *fakeTaskCanceler) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.canceled...)
}

// fakeTaskGetter serves canned mcp_tasks rows, or sql.ErrNoRows for an
// unknown ID — the same "not found" signal *db.Queries.GetMCPTask gives.
type fakeTaskGetter struct {
	mu    sync.Mutex
	tasks map[string]db.McpTask
}

func newFakeTaskGetter() *fakeTaskGetter {
	return &fakeTaskGetter{tasks: make(map[string]db.McpTask)}
}

func (f *fakeTaskGetter) GetMCPTask(_ context.Context, id string) (db.McpTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	task, ok := f.tasks[id]
	if !ok {
		return db.McpTask{}, sql.ErrNoRows
	}
	return task, nil
}

func (f *fakeTaskGetter) set(task db.McpTask) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[task.ID] = task
}

func strPtr(s string) *string { return &s }

// TestTaskWaiter_Wait_AlreadyResolved proves the registration-race fallback:
// a row that is already terminal by the time Wait registers for it (the
// resolution beat the registration — a fast plugin, or an operator's click
// landing inside that same instant) is still delivered correctly, for every
// terminal outcome mcp.PollScheduler can report.
func TestTaskWaiter_Wait_AlreadyResolved(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		wantErr error // nil means "no error" (a completed task)
	}{
		{name: "complete", status: "complete", wantErr: nil},
		{name: "cancelled", status: "cancelled", wantErr: mcp.ErrTaskCanceled},
		{name: "expired", status: "expired", wantErr: mcp.ErrTaskExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getter := newFakeTaskGetter()
			getter.set(db.McpTask{ID: "row-1", Status: tc.status, Result: strPtr(`{"optionId":"approve"}`)})
			waiter := run.NewTaskWaiter(&fakeTaskCanceler{}, getter)

			task, err := waiter.Wait(context.Background(), "row-1", time.Second)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if task.ID != "row-1" {
				t.Errorf("task.ID = %q, want row-1", task.ID)
			}
		})
	}

	t.Run("failed", func(t *testing.T) {
		getter := newFakeTaskGetter()
		getter.set(db.McpTask{ID: "row-1", Status: "failed"})
		waiter := run.NewTaskWaiter(&fakeTaskCanceler{}, getter)

		var failed *mcp.TaskFailedError
		_, err := waiter.Wait(context.Background(), "row-1", time.Second)
		if !errors.As(err, &failed) {
			t.Fatalf("err = %v, want *mcp.TaskFailedError", err)
		}
	})
}

// TestTaskWaiter_Wait_DeliversViaOnResolved proves the live-delivery path:
// OnResolved (the mcp.WithOnResolved hook) wakes a Wait call that is
// currently blocked. OnResolved is fired on a retrying ticker rather than
// once, because TaskWaiter exposes no "registration complete" signal to
// synchronize on directly — a repeated, idempotent signal is the documented
// fallback (docs/developer/testing-patterns.md) for exactly this shape of
// race, and it is safe here because a delivered/timed-out wait unregisters
// itself, making every OnResolved call after the first a harmless no-op.
func TestTaskWaiter_Wait_DeliversViaOnResolved(t *testing.T) {
	getter := newFakeTaskGetter()
	getter.set(db.McpTask{ID: "row-1", Status: "working"})
	waiter := run.NewTaskWaiter(&fakeTaskCanceler{}, getter)

	type result struct {
		task db.McpTask
		err  error
	}
	done := make(chan result, 1)
	go func() {
		task, err := waiter.Wait(context.Background(), "row-1", 5*time.Second)
		done <- result{task: task, err: err}
	}()

	resolved := db.McpTask{ID: "row-1", Status: "complete", Result: strPtr(`{"optionId":"approve"}`)}
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(2 * time.Second)

	for {
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatalf("unexpected error: %v", r.err)
			}
			if r.task.ID != "row-1" {
				t.Errorf("task.ID = %q, want row-1", r.task.ID)
			}
			return
		case <-ticker.C:
			waiter.OnResolved(context.Background(), resolved, nil)
		case <-deadline:
			t.Fatal("Wait did not observe OnResolved within the deadline")
		}
	}
}

// TestTaskWaiter_Wait_Timeout proves that nothing resolving before the
// caller's own deadline ends the wait with ErrTaskWaitTimeout, distinct from
// a server-declared TTL expiry.
func TestTaskWaiter_Wait_Timeout(t *testing.T) {
	getter := newFakeTaskGetter()
	getter.set(db.McpTask{ID: "row-1", Status: "working"})
	waiter := run.NewTaskWaiter(&fakeTaskCanceler{}, getter)

	_, err := waiter.Wait(context.Background(), "row-1", 10*time.Millisecond)
	if !errors.Is(err, run.ErrTaskWaitTimeout) {
		t.Fatalf("err = %v, want ErrTaskWaitTimeout", err)
	}
}

// TestTaskWaiter_Wait_ContextCancelled proves the run's own cancellation ends
// the wait immediately rather than riding out the deadline.
func TestTaskWaiter_Wait_ContextCancelled(t *testing.T) {
	getter := newFakeTaskGetter()
	getter.set(db.McpTask{ID: "row-1", Status: "working"})
	waiter := run.NewTaskWaiter(&fakeTaskCanceler{}, getter)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := waiter.Wait(ctx, "row-1", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestTaskWaiter_Cancel_DelegatesToCanceler proves Cancel is a thin pass
// through to the configured TaskCanceler (mcp.PollScheduler in production).
func TestTaskWaiter_Cancel_DelegatesToCanceler(t *testing.T) {
	canceler := &fakeTaskCanceler{}
	waiter := run.NewTaskWaiter(canceler, newFakeTaskGetter())

	if err := waiter.Cancel(context.Background(), "row-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := canceler.calls(); len(got) != 1 || got[0] != "row-1" {
		t.Errorf("canceler.calls() = %v, want [row-1]", got)
	}
}

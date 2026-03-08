package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rapp992/gleipnir/internal/model"
)

// legalTransitions lists every edge that must succeed.
var legalTransitions = [][2]model.RunStatus{
	{model.RunStatusPending, model.RunStatusRunning},
	{model.RunStatusPending, model.RunStatusFailed},
	{model.RunStatusRunning, model.RunStatusComplete},
	{model.RunStatusRunning, model.RunStatusFailed},
	{model.RunStatusRunning, model.RunStatusWaitingForApproval},
	{model.RunStatusRunning, model.RunStatusInterrupted},
	{model.RunStatusWaitingForApproval, model.RunStatusRunning},
	{model.RunStatusWaitingForApproval, model.RunStatusFailed},
	{model.RunStatusWaitingForApproval, model.RunStatusInterrupted},
}

// illegalTransitions lists a representative set of edges that must fail.
var illegalTransitions = [][2]model.RunStatus{
	{model.RunStatusPending, model.RunStatusComplete},
	{model.RunStatusPending, model.RunStatusWaitingForApproval},
	{model.RunStatusPending, model.RunStatusInterrupted},
	{model.RunStatusComplete, model.RunStatusRunning},
	{model.RunStatusComplete, model.RunStatusFailed},
	{model.RunStatusFailed, model.RunStatusRunning},
	{model.RunStatusFailed, model.RunStatusComplete},
	{model.RunStatusInterrupted, model.RunStatusRunning},
	{model.RunStatusInterrupted, model.RunStatusComplete},
	{model.RunStatusRunning, model.RunStatusPending},
}

func TestRunStateMachine_LegalTransitions(t *testing.T) {
	for _, pair := range legalTransitions {
		from, to := pair[0], pair[1]
		t.Run(string(from)+"→"+string(to), func(t *testing.T) {
			s := newTestStore(t)
			insertPolicy(t, s, "p1")
			insertRun(t, s, "run1", "p1", string(from))

			sm := NewRunStateMachine("run1", from, s.Queries)

			errMsg := ""
			if to == model.RunStatusFailed || to == model.RunStatusInterrupted {
				errMsg = "test error"
			}

			if err := sm.Transition(context.Background(), to, errMsg); err != nil {
				t.Fatalf("Transition(%s → %s): unexpected error: %v", from, to, err)
			}
			if got := sm.Current(); got != to {
				t.Errorf("Current() = %s, want %s", got, to)
			}
		})
	}
}

func TestRunStateMachine_IllegalTransitions(t *testing.T) {
	for _, pair := range illegalTransitions {
		from, to := pair[0], pair[1]
		t.Run(string(from)+"→"+string(to), func(t *testing.T) {
			s := newTestStore(t)
			insertPolicy(t, s, "p1")
			insertRun(t, s, "run1", "p1", string(from))

			sm := NewRunStateMachine("run1", from, s.Queries)

			if err := sm.Transition(context.Background(), to, ""); err == nil {
				t.Errorf("Transition(%s → %s): expected error, got nil", from, to)
			}
			// Status must remain unchanged after a rejected transition.
			if got := sm.Current(); got != from {
				t.Errorf("Current() = %s after illegal transition, want %s", got, from)
			}
		})
	}
}

func TestRunStateMachine_TerminalStatusSetsCompletedAt(t *testing.T) {
	terminalCases := []struct {
		to     model.RunStatus
		errMsg string
	}{
		{model.RunStatusComplete, ""},
		{model.RunStatusFailed, "something went wrong"},
		{model.RunStatusInterrupted, "process restarted"},
	}

	for _, tc := range terminalCases {
		t.Run(string(tc.to), func(t *testing.T) {
			s := newTestStore(t)
			insertPolicy(t, s, "p1")
			insertRun(t, s, "run1", "p1", "running")

			sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries)

			if err := sm.Transition(context.Background(), tc.to, tc.errMsg); err != nil {
				t.Fatalf("Transition: %v", err)
			}

			run, err := s.GetRun(context.Background(), "run1")
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if run.CompletedAt == nil {
				t.Errorf("completed_at is nil, want non-nil for terminal status %s", tc.to)
			}
		})
	}
}

func TestRunStateMachine_NonTerminalStatusLeavesCompletedAtNil(t *testing.T) {
	// pending → running is the only non-terminal transition from a non-running state.
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "run1", "p1", "pending")

	sm := NewRunStateMachine("run1", model.RunStatusPending, s.Queries)

	if err := sm.Transition(context.Background(), model.RunStatusRunning, ""); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	run, err := s.GetRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.CompletedAt != nil {
		t.Errorf("completed_at = %v, want nil for non-terminal status running", run.CompletedAt)
	}
}

func TestRunStateMachine_FailedTransitionSetsErrorColumn(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "run1", "p1", "running")

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries)
	const wantMsg = "something bad happened"

	if err := sm.Transition(context.Background(), model.RunStatusFailed, wantMsg); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	run, err := s.GetRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Error == nil {
		t.Fatal("error column is nil, want non-nil")
	}
	if *run.Error != wantMsg {
		t.Errorf("error column = %q, want %q", *run.Error, wantMsg)
	}
}

func TestRunStateMachine_ConcurrentTransitions(t *testing.T) {
	// N goroutines race to transition running → complete. Exactly one must
	// succeed; all others must get an "illegal transition" error (since the
	// state is already complete after the first winner).
	const N = 20

	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "run1", "p1", "running")

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries)

	var (
		wg        sync.WaitGroup
		successes atomic.Int64
	)
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			if err := sm.Transition(context.Background(), model.RunStatusComplete, ""); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Errorf("concurrent transitions succeeded %d times, want exactly 1", got)
	}
	if got := sm.Current(); got != model.RunStatusComplete {
		t.Errorf("final status = %s, want complete", got)
	}
}

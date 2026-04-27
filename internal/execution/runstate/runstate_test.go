package runstate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// capturePublisher records every Publish call for assertion in tests.
type capturePublisher struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	eventType string
	data      json.RawMessage
}

func (p *capturePublisher) Publish(eventType string, data json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, capturedEvent{eventType: eventType, data: data})
}

func (p *capturePublisher) all() []capturedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]capturedEvent, len(p.events))
	copy(out, p.events)
	return out
}

func TestTransitionRunFailed_LegalTransitions(t *testing.T) {
	legalSources := []model.RunStatus{
		model.RunStatusPending,
		model.RunStatusRunning,
		model.RunStatusWaitingForApproval,
		model.RunStatusWaitingForFeedback,
	}

	for _, from := range legalSources {
		t.Run(string(from), func(t *testing.T) {
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", from)

			const reason = "timed out"
			if err := TransitionRunFailed(context.Background(), s.Queries(), nil, "run1", reason); err != nil {
				t.Fatalf("TransitionRunFailed(%s → failed): unexpected error: %v", from, err)
			}

			run, err := s.Queries().GetRun(context.Background(), "run1")
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if run.Status != string(model.RunStatusFailed) {
				t.Errorf("status = %q, want %q", run.Status, model.RunStatusFailed)
			}
			if run.CompletedAt == nil {
				t.Error("completed_at is nil, want non-nil for failed status")
			}
			if run.Error == nil {
				t.Fatal("error column is nil, want non-nil")
			}
			if *run.Error != reason {
				t.Errorf("error = %q, want %q", *run.Error, reason)
			}
		})
	}
}

func TestTransitionRunFailed_IllegalTransitions(t *testing.T) {
	illegalSources := []model.RunStatus{
		model.RunStatusComplete,
		model.RunStatusFailed,
		model.RunStatusInterrupted,
	}

	for _, from := range illegalSources {
		t.Run(string(from), func(t *testing.T) {
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", from)

			err := TransitionRunFailed(context.Background(), s.Queries(), nil, "run1", "reason")
			if err == nil {
				t.Fatalf("TransitionRunFailed(%s → failed): expected error, got nil", from)
			}
			if !errors.Is(err, ErrIllegalTransition) {
				t.Errorf("error = %q, want errors.Is(err, ErrIllegalTransition)", err)
			}

			// Status must remain unchanged.
			run, err := s.Queries().GetRun(context.Background(), "run1")
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if run.Status != string(from) {
				t.Errorf("status = %q after illegal transition, want %q", run.Status, from)
			}
		})
	}
}

func TestTransitionRunFailed_PublishesEvent(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	if err := TransitionRunFailed(context.Background(), s.Queries(), pub, "run1", "timeout"); err != nil {
		t.Fatalf("TransitionRunFailed: %v", err)
	}

	events := pub.all()
	if len(events) != 1 {
		t.Fatalf("got %d published events, want 1", len(events))
	}
	if events[0].eventType != "run.status_changed" {
		t.Errorf("event type = %q, want %q", events[0].eventType, "run.status_changed")
	}

	var payload map[string]string
	if err := json.Unmarshal(events[0].data, &payload); err != nil {
		t.Fatalf("unmarshal event data: %v", err)
	}
	if payload["run_id"] != "run1" {
		t.Errorf("run_id = %q, want %q", payload["run_id"], "run1")
	}
	if payload["status"] != "failed" {
		t.Errorf("status = %q, want %q", payload["status"], "failed")
	}
}

func TestTransitionRunFailed_NilPublisher(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	// nil publisher must not panic.
	if err := TransitionRunFailed(context.Background(), s.Queries(), nil, "run1", "reason"); err != nil {
		t.Fatalf("TransitionRunFailed with nil publisher: %v", err)
	}
}

func TestTransitionRunFailed_RunNotFound(t *testing.T) {
	s := testutil.NewTestStore(t)

	err := TransitionRunFailed(context.Background(), s.Queries(), nil, "nonexistent-run", "reason")
	if err == nil {
		t.Fatal("expected error for nonexistent run, got nil")
	}
}

func TestIsLegalTransition(t *testing.T) {
	legal := [][2]model.RunStatus{
		{model.RunStatusPending, model.RunStatusRunning},
		{model.RunStatusPending, model.RunStatusFailed},
		{model.RunStatusRunning, model.RunStatusComplete},
		{model.RunStatusRunning, model.RunStatusFailed},
		{model.RunStatusRunning, model.RunStatusWaitingForApproval},
		{model.RunStatusRunning, model.RunStatusWaitingForFeedback},
		{model.RunStatusRunning, model.RunStatusInterrupted},
		{model.RunStatusWaitingForApproval, model.RunStatusRunning},
		{model.RunStatusWaitingForApproval, model.RunStatusFailed},
		{model.RunStatusWaitingForApproval, model.RunStatusInterrupted},
		{model.RunStatusWaitingForFeedback, model.RunStatusRunning},
		{model.RunStatusWaitingForFeedback, model.RunStatusFailed},
		{model.RunStatusWaitingForFeedback, model.RunStatusInterrupted},
	}
	for _, pair := range legal {
		if !IsLegalTransition(pair[0], pair[1]) {
			t.Errorf("IsLegalTransition(%s, %s) = false, want true", pair[0], pair[1])
		}
	}

	illegal := [][2]model.RunStatus{
		{model.RunStatusComplete, model.RunStatusRunning},
		{model.RunStatusComplete, model.RunStatusFailed},
		{model.RunStatusFailed, model.RunStatusRunning},
		{model.RunStatusInterrupted, model.RunStatusRunning},
		{model.RunStatusRunning, model.RunStatusPending},
	}
	for _, pair := range illegal {
		if IsLegalTransition(pair[0], pair[1]) {
			t.Errorf("IsLegalTransition(%s, %s) = true, want false", pair[0], pair[1])
		}
	}
}

func TestTerminalStates_NoOutgoingTransitions(t *testing.T) {
	allStatuses := []model.RunStatus{
		model.RunStatusPending,
		model.RunStatusRunning,
		model.RunStatusComplete,
		model.RunStatusFailed,
		model.RunStatusWaitingForApproval,
		model.RunStatusWaitingForFeedback,
		model.RunStatusInterrupted,
	}

	for _, from := range allStatuses {
		if !model.IsTerminalStatus(from) {
			continue
		}
		for _, to := range allStatuses {
			if IsLegalTransition(from, to) {
				t.Errorf("IsLegalTransition(%s, %s) = true, want false: terminal states have no outgoing transitions", from, to)
			}
		}
	}
}

// TestTransitionRunFailed_CASConflict verifies the CAS semantics of TransitionRunFailed.
// TransitionRunFailed reads the current version via GetRun then updates via CAS. On a
// single-connection DB, interposing between those two calls is not possible without
// mocking, so we verify the CAS invariant directly at the db.Queries layer below.
func TestTransitionRunFailed_CASConflict(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	// Bump the version so we can verify the CAS reads it correctly.
	testutil.SetRunVersion(t, s, "run1", 5)

	// Verify a normal call works — it reads version 5, CAS updates with version 5,
	// and the row ends up at version 6.
	if err := TransitionRunFailed(context.Background(), s.Queries(), nil, "run1", "first"); err != nil {
		t.Fatalf("first TransitionRunFailed: unexpected error: %v", err)
	}
	run, err := s.Queries().GetRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetRun after first transition: %v", err)
	}
	if run.Status != string(model.RunStatusFailed) {
		t.Errorf("status = %q, want failed", run.Status)
	}
	if run.Version != 6 {
		t.Errorf("version = %d, want 6", run.Version)
	}

	// Reset the run to running, set version=10, then use UpdateRunError with
	// expected_version=9 (stale) — this is the exact CAS miss condition.
	_, resetErr := s.DB().Exec(`UPDATE runs SET status = 'running', version = 10, completed_at = NULL, error = NULL WHERE id = 'run1'`)
	if resetErr != nil {
		t.Fatalf("reset run state: %v", resetErr)
	}

	completedAt := "2099-01-01T00:00:00Z"
	reason := "stale version"
	// Using expected_version=9 when the real version is 10 — this is a CAS miss.
	rows, updateErr := s.Queries().UpdateRunError(context.Background(), db.UpdateRunErrorParams{
		Status:          string(model.RunStatusFailed),
		Error:           &reason,
		CompletedAt:     &completedAt,
		ID:              "run1",
		ExpectedVersion: 9, // stale
	})
	if updateErr != nil {
		t.Fatalf("UpdateRunError: %v", updateErr)
	}
	if rows != 0 {
		t.Errorf("rows = %d for stale version CAS, want 0", rows)
	}

	// Run status must be unchanged (still running).
	afterMiss, err := s.Queries().GetRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetRun after CAS miss: %v", err)
	}
	if afterMiss.Status != string(model.RunStatusRunning) {
		t.Errorf("status = %q after CAS miss, want running", afterMiss.Status)
	}
	if afterMiss.Version != 10 {
		t.Errorf("version = %d after CAS miss, want 10 (unchanged)", afterMiss.Version)
	}
}

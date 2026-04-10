package runstate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
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

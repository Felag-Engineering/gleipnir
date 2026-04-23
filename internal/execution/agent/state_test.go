package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rapp992/gleipnir/internal/execution/runstate"
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

// countByType returns the number of events with the given eventType.
func (p *capturePublisher) countByType(eventType string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.events {
		if e.eventType == eventType {
			n++
		}
	}
	return n
}

// countByStatus returns the number of eventType events whose JSON payload has
// a "status" field equal to the given value. Used to distinguish e.g. the
// waiting_for_approval transition event from the failed transition event.
func (p *capturePublisher) countByStatus(eventType, status string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.events {
		if e.eventType != eventType {
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal(e.data, &payload); err == nil && payload["status"] == status {
			n++
		}
	}
	return n
}

// legalTransitions lists every edge that must succeed.
var legalTransitions = [][2]model.RunStatus{
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

// illegalTransitions lists a representative set of edges that must fail.
var illegalTransitions = [][2]model.RunStatus{
	{model.RunStatusPending, model.RunStatusComplete},
	{model.RunStatusPending, model.RunStatusWaitingForApproval},
	{model.RunStatusPending, model.RunStatusWaitingForFeedback},
	{model.RunStatusPending, model.RunStatusInterrupted},
	{model.RunStatusComplete, model.RunStatusRunning},
	{model.RunStatusComplete, model.RunStatusFailed},
	{model.RunStatusComplete, model.RunStatusWaitingForFeedback},
	{model.RunStatusFailed, model.RunStatusRunning},
	{model.RunStatusFailed, model.RunStatusComplete},
	{model.RunStatusInterrupted, model.RunStatusRunning},
	{model.RunStatusInterrupted, model.RunStatusComplete},
	{model.RunStatusWaitingForFeedback, model.RunStatusComplete},
	{model.RunStatusRunning, model.RunStatusPending},
}

func TestRunStateMachine_LegalTransitions(t *testing.T) {
	for _, pair := range legalTransitions {
		from, to := pair[0], pair[1]
		t.Run(string(from)+"→"+string(to), func(t *testing.T) {
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", from)

			sm := NewRunStateMachine("run1", from, s.DB(), s.Queries())

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
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", from)

			sm := NewRunStateMachine("run1", from, s.DB(), s.Queries())

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
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

			sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())

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
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusPending)

	sm := NewRunStateMachine("run1", model.RunStatusPending, s.DB(), s.Queries())

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
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
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

func TestRunStateMachine_PublishesOnSuccessfulTransition(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusPending)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusPending, s.DB(), s.Queries(), WithStateMachinePublisher(pub))

	if err := sm.Transition(context.Background(), model.RunStatusRunning, ""); err != nil {
		t.Fatalf("Transition: %v", err)
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
	if payload["status"] != string(model.RunStatusRunning) {
		t.Errorf("status = %q, want %q", payload["status"], model.RunStatusRunning)
	}
}

func TestRunStateMachine_NoPublishOnIllegalTransition(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusPending)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusPending, s.DB(), s.Queries(), WithStateMachinePublisher(pub))

	// pending → complete is illegal.
	_ = sm.Transition(context.Background(), model.RunStatusComplete, "")

	if events := pub.all(); len(events) != 0 {
		t.Errorf("got %d published events after illegal transition, want 0", len(events))
	}
}

func TestRunStateMachine_NilPublisherIsSafe(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusPending)

	// No publisher option — should not panic.
	sm := NewRunStateMachine("run1", model.RunStatusPending, s.DB(), s.Queries())

	if err := sm.Transition(context.Background(), model.RunStatusRunning, ""); err != nil {
		t.Fatalf("Transition with nil publisher: %v", err)
	}
}

func TestRunStateMachine_ConcurrentTransitions(t *testing.T) {
	// N goroutines race to transition running → complete. Exactly one must
	// succeed; all others must get an "illegal transition" error (since the
	// state is already complete after the first winner).
	const N = 20

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())

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

func TestRunStateMachine_WaitingForApproval_CreatesRecord(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))

	payload := ApprovalPayload{
		ApprovalID:    "approval-001",
		ToolName:      "my-server.do_thing",
		ProposedInput: `{"key":"value"}`,
		ExpiresAt:     "2099-01-01T00:00:00Z",
	}

	if err := sm.Transition(context.Background(), model.RunStatusWaitingForApproval, "", WithApprovalPayload(payload)); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	// Verify the approval_requests DB record was created.
	pending, err := s.GetPendingApprovalRequestsByRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetPendingApprovalRequestsByRun: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending approval requests = %d, want 1", len(pending))
	}
	if pending[0].ID != "approval-001" {
		t.Errorf("approval request ID = %q, want %q", pending[0].ID, "approval-001")
	}

	// Verify both run.status_changed and approval.created events were published.
	events := pub.all()
	var hasStatusChanged, hasApprovalCreated bool
	var approvalCreatedPayload map[string]string
	for _, ev := range events {
		switch ev.eventType {
		case "run.status_changed":
			hasStatusChanged = true
		case "approval.created":
			hasApprovalCreated = true
			if err := json.Unmarshal(ev.data, &approvalCreatedPayload); err != nil {
				t.Fatalf("unmarshal approval.created data: %v", err)
			}
		}
	}
	if !hasStatusChanged {
		t.Error("expected run.status_changed event, not found")
	}
	if !hasApprovalCreated {
		t.Fatal("expected approval.created event, not found")
	}
	if approvalCreatedPayload["approval_id"] != "approval-001" {
		t.Errorf("approval.created approval_id = %q, want %q", approvalCreatedPayload["approval_id"], "approval-001")
	}
	if approvalCreatedPayload["run_id"] != "run1" {
		t.Errorf("approval.created run_id = %q, want %q", approvalCreatedPayload["run_id"], "run1")
	}
}

func TestRunStateMachine_WaitingForApproval_NoPayload(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))

	// Transition without a payload — should succeed with only run.status_changed.
	if err := sm.Transition(context.Background(), model.RunStatusWaitingForApproval, ""); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	events := pub.all()
	if len(events) != 1 {
		t.Fatalf("got %d published events, want 1", len(events))
	}
	if events[0].eventType != "run.status_changed" {
		t.Errorf("event type = %q, want %q", events[0].eventType, "run.status_changed")
	}

	// No approval_requests record should have been created.
	pending, err := s.GetPendingApprovalRequestsByRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetPendingApprovalRequestsByRun: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending approval requests = %d, want 0", len(pending))
	}
}

func TestRunStateMachine_WaitingForFeedback_CreatesRecord(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))

	payload := FeedbackPayload{
		FeedbackID:    "feedback-001",
		ToolName:      "slack.send_message",
		ProposedInput: `{"channel":"ops","text":"Should I proceed?"}`,
		Message:       "Message sent to #ops",
	}

	if err := sm.Transition(context.Background(), model.RunStatusWaitingForFeedback, "", WithFeedbackPayload(payload)); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	// Verify the feedback_requests DB record was created.
	pending, err := s.GetPendingFeedbackRequestsByRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetPendingFeedbackRequestsByRun: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending feedback requests = %d, want 1", len(pending))
	}
	if pending[0].ID != "feedback-001" {
		t.Errorf("feedback request ID = %q, want %q", pending[0].ID, "feedback-001")
	}
	if pending[0].ToolName != "slack.send_message" {
		t.Errorf("tool_name = %q, want %q", pending[0].ToolName, "slack.send_message")
	}

	// Verify both run.status_changed and feedback.created events were published.
	events := pub.all()
	var hasStatusChanged, hasFeedbackCreated bool
	var feedbackCreatedPayload map[string]string
	for _, ev := range events {
		switch ev.eventType {
		case "run.status_changed":
			hasStatusChanged = true
		case "feedback.created":
			hasFeedbackCreated = true
			if err := json.Unmarshal(ev.data, &feedbackCreatedPayload); err != nil {
				t.Fatalf("unmarshal feedback.created data: %v", err)
			}
		}
	}
	if !hasStatusChanged {
		t.Error("expected run.status_changed event, not found")
	}
	if !hasFeedbackCreated {
		t.Fatal("expected feedback.created event, not found")
	}
	if feedbackCreatedPayload["feedback_id"] != "feedback-001" {
		t.Errorf("feedback.created feedback_id = %q, want %q", feedbackCreatedPayload["feedback_id"], "feedback-001")
	}
	if feedbackCreatedPayload["run_id"] != "run1" {
		t.Errorf("feedback.created run_id = %q, want %q", feedbackCreatedPayload["run_id"], "run1")
	}
}

func TestRunStateMachine_WaitingForFeedback_NoPayload(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))

	// Transition without a payload — should succeed with only run.status_changed.
	if err := sm.Transition(context.Background(), model.RunStatusWaitingForFeedback, ""); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	events := pub.all()
	if len(events) != 1 {
		t.Fatalf("got %d published events, want 1", len(events))
	}
	if events[0].eventType != "run.status_changed" {
		t.Errorf("event type = %q, want %q", events[0].eventType, "run.status_changed")
	}

	// No feedback_requests record should have been created.
	pending, err := s.GetPendingFeedbackRequestsByRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetPendingFeedbackRequestsByRun: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending feedback requests = %d, want 0", len(pending))
	}
}

func TestRunStateMachine_PersistSystemPrompt(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusPending)

	sm := NewRunStateMachine("run1", model.RunStatusPending, s.DB(), s.Queries())

	prompt := "You are a helpful agent.\n\nCapabilities:\n- read files"
	if err := sm.PersistSystemPrompt(context.Background(), prompt); err != nil {
		t.Fatalf("PersistSystemPrompt: unexpected error: %v", err)
	}

	// Read back from the DB and verify the value was persisted.
	row := s.DB().QueryRow(`SELECT system_prompt FROM runs WHERE id = 'run1'`)
	var got *string
	if err := row.Scan(&got); err != nil {
		t.Fatalf("scan system_prompt: %v", err)
	}
	if got == nil {
		t.Fatal("system_prompt is NULL, want non-nil")
	}
	if *got != prompt {
		t.Errorf("system_prompt = %q, want %q", *got, prompt)
	}
}

// TestRunStateMachine_TransactionRollback verifies that when the secondary INSERT
// inside a transition fails, the status UPDATE is rolled back atomically.
// A duplicate approval ID (PRIMARY KEY violation) is used to trigger the failure.
func TestRunStateMachine_TransactionRollback(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())

	payload := ApprovalPayload{
		ApprovalID:    "approval-dup",
		ToolName:      "my-server.do_thing",
		ProposedInput: `{"key":"value"}`,
		ExpiresAt:     "2099-01-01T00:00:00Z",
	}

	// First transition: succeeds, creates the approval record.
	if err := sm.Transition(context.Background(), model.RunStatusWaitingForApproval, "", WithApprovalPayload(payload)); err != nil {
		t.Fatalf("first Transition: unexpected error: %v", err)
	}

	// Move back to running so we can attempt the same waiting_for_approval transition
	// with the duplicate approval ID.
	if err := sm.Transition(context.Background(), model.RunStatusRunning, ""); err != nil {
		t.Fatalf("Transition back to running: %v", err)
	}

	// Second transition with SAME approval ID — INSERT INTO approval_requests will
	// violate the PRIMARY KEY constraint. The transaction must roll back the status UPDATE.
	err := sm.Transition(context.Background(), model.RunStatusWaitingForApproval, "", WithApprovalPayload(payload))
	if err == nil {
		t.Fatal("expected error from duplicate approval ID, got nil")
	}

	// The run status must still be 'running' — the UPDATE was rolled back.
	run, getErr := s.GetRun(context.Background(), "run1")
	if getErr != nil {
		t.Fatalf("GetRun: %v", getErr)
	}
	if run.Status != string(model.RunStatusRunning) {
		t.Errorf("status = %q after rolled-back transition, want %q", run.Status, model.RunStatusRunning)
	}

	// In-memory state must also remain 'running'.
	if got := sm.Current(); got != model.RunStatusRunning {
		t.Errorf("Current() = %s after rolled-back transition, want running", got)
	}

	// Version must not have advanced.
	// After the successful running→waiting_for_approval→running round-trip, version is 2.
	if got := sm.Version(); got != 2 {
		t.Errorf("Version() = %d after rolled-back transition, want 2", got)
	}

	// Only one approval_requests row must exist (the first successful one).
	pending, err := s.GetPendingApprovalRequestsByRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetPendingApprovalRequestsByRun: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("approval_requests count = %d after rollback, want 1", len(pending))
	}
}

// TestRunStateMachine_CASConflict verifies that when two RunStateMachine instances
// point at the same DB row, exactly one transition succeeds and the other returns
// ErrTransitionConflict.
//
// Key invariant tested here: IsLegalTransition(running, failed) returns true for
// BOTH instances — each holds running as its in-memory state. The CAS guard on
// the version column is what rejects the loser, not the legality check.
func TestRunStateMachine_CASConflict(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	// Both instances start with running state and version 0.
	sm1 := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	sm2 := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())

	// Explicitly verify that IsLegalTransition passes for both intended targets
	// so we know the CAS — not the legality check — is what distinguishes them.
	if !runstate.IsLegalTransition(model.RunStatusRunning, model.RunStatusComplete) {
		t.Fatal("IsLegalTransition(running, complete) must be true for this test to be meaningful")
	}
	if !runstate.IsLegalTransition(model.RunStatusRunning, model.RunStatusFailed) {
		t.Fatal("IsLegalTransition(running, failed) must be true for this test to be meaningful")
	}

	// sm1 wins: running → complete.
	if err := sm1.Transition(context.Background(), model.RunStatusComplete, ""); err != nil {
		t.Fatalf("sm1.Transition to complete: unexpected error: %v", err)
	}

	// sm2 attempts running → failed using the same (stale) version 0.
	// The CAS must reject it.
	err := sm2.Transition(context.Background(), model.RunStatusFailed, "conflict test")
	if err == nil {
		t.Fatal("sm2.Transition: expected ErrTransitionConflict, got nil")
	}
	if !errors.Is(err, ErrTransitionConflict) {
		t.Errorf("sm2.Transition: got %v, want errors.Is(err, ErrTransitionConflict)", err)
	}

	// The DB row must reflect sm1's outcome: complete.
	run, getErr := s.GetRun(context.Background(), "run1")
	if getErr != nil {
		t.Fatalf("GetRun: %v", getErr)
	}
	if run.Status != string(model.RunStatusComplete) {
		t.Errorf("DB status = %q, want complete", run.Status)
	}
	if run.Version != 1 {
		t.Errorf("DB version = %d, want 1 (exactly one committed transition)", run.Version)
	}
}

// TestRunStateMachine_ConcurrentTransitionsAcrossInstances creates N independent
// RunStateMachine instances on the same DB row and races them. Exactly one must
// win; all losers must return ErrTransitionConflict. The final DB status must be
// exactly one of the attempted terminal states.
func TestRunStateMachine_ConcurrentTransitionsAcrossInstances(t *testing.T) {
	const N = 10

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	// Targets the goroutines will attempt — mix of complete and failed to confirm
	// the test doesn't accidentally rely on one specific status winning.
	targets := []model.RunStatus{model.RunStatusComplete, model.RunStatusFailed}
	legalTargets := map[model.RunStatus]bool{
		model.RunStatusComplete: true,
		model.RunStatusFailed:   true,
	}

	var (
		wg        sync.WaitGroup
		successes atomic.Int64
		conflicts atomic.Int64
	)
	wg.Add(N)
	for i := range N {
		target := targets[i%len(targets)]
		sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
		go func(sm *RunStateMachine, to model.RunStatus) {
			defer wg.Done()
			errMsg := ""
			if to == model.RunStatusFailed {
				errMsg = "concurrent failure"
			}
			err := sm.Transition(context.Background(), to, errMsg)
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, ErrTransitionConflict) {
				conflicts.Add(1)
			}
			// Any other error is a test failure — we don't increment either counter.
		}(sm, target)
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Errorf("exactly 1 goroutine should have succeeded; got %d", got)
	}
	if got := conflicts.Load(); got != int64(N-1) {
		t.Errorf("exactly %d goroutines should have returned ErrTransitionConflict; got %d", N-1, got)
	}

	// DB row must have exactly one terminal status from the set of attempted targets.
	run, err := s.GetRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !legalTargets[model.RunStatus(run.Status)] {
		t.Errorf("DB status = %q is not in the set of legal target statuses; want one of {complete, failed}", run.Status)
	}
	if run.Version != 1 {
		t.Errorf("DB version = %d, want 1 (exactly one committed transition)", run.Version)
	}
}

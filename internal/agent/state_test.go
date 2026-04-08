package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
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

			sm := NewRunStateMachine("run1", from, s.Queries())

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

			sm := NewRunStateMachine("run1", from, s.Queries())

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

			sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries())

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

	sm := NewRunStateMachine("run1", model.RunStatusPending, s.Queries())

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

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries())
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
	sm := NewRunStateMachine("run1", model.RunStatusPending, s.Queries(), WithStateMachinePublisher(pub))

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
	sm := NewRunStateMachine("run1", model.RunStatusPending, s.Queries(), WithStateMachinePublisher(pub))

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
	sm := NewRunStateMachine("run1", model.RunStatusPending, s.Queries())

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

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries())

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
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries(), WithStateMachinePublisher(pub))

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
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries(), WithStateMachinePublisher(pub))

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
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries(), WithStateMachinePublisher(pub))

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
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries(), WithStateMachinePublisher(pub))

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

	sm := NewRunStateMachine("run1", model.RunStatusPending, s.Queries())

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

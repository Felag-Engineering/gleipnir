// Package agent — inputrequired_test.go pins the tool-initiated HITL contract
// (ADR-055, spec §6 source 3): the pause is durable before it is observable,
// the operator's answer is replayed onto the ORIGINAL call, and the agent's
// trace never shows that any of it happened.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// testResponder is the authenticated operator these tests answer as, unless a
// test is specifically about the responder identity itself. The matching
// users row is inserted by insertTestResponderUser: decision records verify
// ActorUserID against a real FK, so a decision record naming this ID needs the
// row to exist.
var testResponder = Responder{UserID: "u-alice", Username: "alice"}

// insertTestResponderUser inserts the users row behind testResponder, so a
// decision record naming ActorUserID = testResponder.UserID satisfies
// plugin_audit_events.actor_user_id's foreign key.
func insertTestResponderUser(t *testing.T, s *db.Store) {
	t.Helper()
	if _, err := s.CreateUser(context.Background(), db.CreateUserParams{
		ID:           testResponder.UserID,
		Username:     testResponder.Username,
		PasswordHash: "x",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("insertTestResponderUser: %v", err)
	}
}

// permissionAsk is an input_required result asking for consent only: the §6.1
// convention is a requestedSchema with no properties.
func permissionAsk(message string) *mcp.InputRequiredResult {
	return &mcp.InputRequiredResult{
		InputRequests: []mcp.InputRequest{{
			Message:         message,
			RequestedSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}},
		RequestState: json.RawMessage(`{"cursor":"abc"}`),
	}
}

// awaitPendingToolInputID blocks until the state machine publishes
// tool_input.created — which fires only after the tool_input_requests INSERT has
// committed, since the INSERT and the status change share one transaction — then
// returns the pending row's ID. Waiting on the pause's own event rather than
// run.status_changed matters on the approval test, where an earlier transition
// would otherwise satisfy the wait before this row exists.
func awaitPendingToolInputID(t *testing.T, pub *capturePublisher, s *db.Store) string {
	t.Helper()
	return awaitNthPendingToolInputID(t, pub, s, 1)
}

// awaitNthPendingToolInputID waits for the nth tool_input.created event, for
// tests that answer several pauses in a row.
func awaitNthPendingToolInputID(t *testing.T, pub *capturePublisher, s *db.Store, n int) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for pub.countByType("tool_input.created") < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for tool_input.created #%d", n)
		}
		time.Sleep(2 * time.Millisecond)
	}
	rows, err := s.ListResumableToolInputRequests(context.Background())
	if err != nil {
		t.Fatalf("ListResumableToolInputRequests: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no pending tool_input_requests row after tool_input.created")
	}
	return rows[0].ID
}

// newRoutingFixture wires the store, run, publisher, and handler shared by the
// Route tests. The run starts running, as it is mid-tool-call.
func newRoutingFixture(t *testing.T, timeout time.Duration) (*db.Store, *capturePublisher, *RunStateMachine, *InputRequiredHandler) {
	t.Helper()
	s := testutil.NewTestStore(t)
	insertTestResponderUser(t, s)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)
	testutil.InsertMcpServer(t, s, "srv1", "myserver", "http://example.invalid")

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { _ = w.Close() })

	return s, pub, sm, NewInputRequiredHandler(w, sm, timeout)
}

func routeRequest(result *mcp.InputRequiredResult, timeout time.Duration) InputRoutingRequest {
	return InputRoutingRequest{
		RunID:    "run1",
		ServerID: "srv1",
		ToolName: "myserver.deploy",
		Input:    map[string]any{"env": "prod"},
		Result:   result,
		Timeout:  timeout,
	}
}

func TestInputRequiredHandler_Route_ResumesOnAnswer(t *testing.T) {
	s, pub, sm, h := newRoutingFixture(t, time.Minute)

	type routeResult struct {
		answers []mcp.InputResponse
		err     error
	}
	done := make(chan routeResult, 1)
	go func() {
		answers, _, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
		done <- routeResult{answers: answers, err: err}
	}()

	requestID := awaitPendingToolInputID(t, pub, s)

	// The pause must be durable and complete before it is answerable: the row
	// carries everything a restart would need to apply an answer.
	row, err := s.GetToolInputRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "pending" {
		t.Errorf("status = %q, want pending", row.Status)
	}
	if row.ServerID != "srv1" || row.ToolName != "myserver.deploy" {
		t.Errorf("server_id/tool_name = %q/%q, want srv1/myserver.deploy", row.ServerID, row.ToolName)
	}
	if row.RequestState != `{"cursor":"abc"}` {
		t.Errorf("request_state = %q, want the server's blob verbatim", row.RequestState)
	}
	if row.CallArgs != `{"env":"prod"}` {
		t.Errorf("call_args = %q, want the original call arguments", row.CallArgs)
	}
	if row.ElicitationKind != string(elicitationKindPermission) {
		t.Errorf("elicitation_kind = %q, want permission", row.ElicitationKind)
	}
	if row.ExpiresAt == "" {
		t.Error("expires_at is empty; every pause must carry a deadline")
	}
	if sm.Current() != model.RunStatusWaitingForFeedback {
		t.Errorf("run status = %s, want waiting_for_feedback", sm.Current())
	}

	if err := h.Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Route: unexpected error: %v", res.err)
		}
		if len(res.answers) != 1 || res.answers[0].Action != inputActionAccept {
			t.Fatalf("answers = %+v, want one accept", res.answers)
		}
		if string(res.answers[0].Content) != `{"confirm":true}` {
			t.Errorf("content = %s, want the operator payload verbatim", res.answers[0].Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Route did not return within deadline")
	}

	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running", sm.Current())
	}

	row, err = s.GetToolInputRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetToolInputRequest after resolve: %v", err)
	}
	if row.Status != "resolved" {
		t.Errorf("status = %q, want resolved", row.Status)
	}
	if row.Response == nil || !strings.Contains(*row.Response, inputActionAccept) {
		t.Errorf("response = %v, want the recorded answers", row.Response)
	}
}

// The deny path is still an MRTR round trip: the declines go back to the
// server, which decides what they mean. The run resumes either way.
func TestInputRequiredHandler_Route_DeclineResumesRun(t *testing.T) {
	s, pub, sm, h := newRoutingFixture(t, time.Minute)

	done := make(chan []mcp.InputResponse, 1)
	errc := make(chan error, 1)
	go func() {
		answers, _, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
		if err != nil {
			errc <- err
			return
		}
		done <- answers
	}()

	requestID := awaitPendingToolInputID(t, pub, s)
	if err := h.Decline(requestID); err != nil {
		t.Fatalf("Decline: %v", err)
	}

	select {
	case answers := <-done:
		if len(answers) != 1 || answers[0].Action != inputActionDecline {
			t.Fatalf("answers = %+v, want one decline", answers)
		}
		if answers[0].Content != nil {
			t.Errorf("declined response carries content %s, want none", answers[0].Content)
		}
	case err := <-errc:
		t.Fatalf("Route: unexpected error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Route did not return within deadline")
	}

	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running — a refusal is an answer, not a failure", sm.Current())
	}
	row, err := s.GetToolInputRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "resolved" {
		t.Errorf("status = %q, want resolved", row.Status)
	}
}

func TestInputRequiredHandler_Route_TimeoutFailsTheWait(t *testing.T) {
	s, _, _, h := newRoutingFixture(t, time.Minute)

	// A 10ms deadline nobody answers. The wait is the thing under test, so the
	// short timeout is the input, not a race against a background actor.
	_, _, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), 10*time.Millisecond))
	if err == nil {
		t.Fatal("Route: want a timeout error, got nil")
	}
	var routeErr *InputRoutingError
	if !errors.As(err, &routeErr) {
		t.Fatalf("Route error = %v, want *InputRoutingError", err)
	}

	rows, err := s.ListResumableToolInputRequests(context.Background())
	if err != nil {
		t.Fatalf("ListResumableToolInputRequests: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d rows still pending after timeout, want 0 — the row must be claimed", len(rows))
	}
}

// When the timeout scanner claims the row first, the waiting side must not
// write a second error step. Expiring the row by hand models the scanner
// winning without needing one to be running.
func TestInputRequiredHandler_Route_TimeoutLostToScanner(t *testing.T) {
	s, pub, _, h := newRoutingFixture(t, time.Minute)

	errc := make(chan error, 1)
	go func() {
		_, _, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), 300*time.Millisecond))
		errc <- err
	}()

	requestID := awaitPendingToolInputID(t, pub, s)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.ExpireToolInputRequest(context.Background(), db.ExpireToolInputRequestParams{ResolvedAt: &now, ID: requestID})
	if err != nil {
		t.Fatalf("ExpireToolInputRequest: %v", err)
	}
	if rows != 1 {
		t.Fatalf("scanner claim affected %d rows, want 1", rows)
	}

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Route: want a timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "already resolved by scanner") {
			t.Errorf("Route error = %v, want the scanner-won sentinel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Route did not return within deadline")
	}
}

// A run cancelled while a tool-initiated request is pending must not leave the
// row 'pending' forever, where the timeout scanner would eventually mislabel
// it 'timed_out' -- a cancellation is neither "nobody answered in time" nor
// "the server discarded its state" (relay-646 §7, ADR-061). Cancelling the
// context models RunManager.Cancel reaching Route mid-wait.
func TestInputRequiredHandler_Route_CtxCancelledMarksRequestCancelled(t *testing.T) {
	s, pub, _, h := newRoutingFixture(t, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, _, err := h.Route(ctx, routeRequest(permissionAsk("deploy to prod?"), time.Minute))
		errc <- err
	}()

	requestID := awaitPendingToolInputID(t, pub, s)
	cancel()

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Route: want a cancellation error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Route did not return within deadline")
	}

	// No longer resumable: the row settled as cancelled rather than being
	// left pending for the scanner to mislabel later.
	rows, err := s.ListResumableToolInputRequests(context.Background())
	if err != nil {
		t.Fatalf("ListResumableToolInputRequests: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("resumable rows = %+v, want none — a cancelled run must not leave the row pending", rows)
	}

	row, err := s.GetToolInputRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", row.Status)
	}
	if row.ResolvedAt == nil {
		t.Error("resolved_at is NULL for a cancelled request")
	}

	records, err := decision.NewRecorder(s.Queries()).ForRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("decision records = %+v, want exactly 1", records)
	}
	if records[0].Outcome != decision.OutcomeCancelled {
		t.Errorf("Outcome = %q, want %q", records[0].Outcome, decision.OutcomeCancelled)
	}
	if records[0].ActorUserID != "" {
		t.Errorf("ActorUserID = %q, want none — nobody acted", records[0].ActorUserID)
	}
}

// The conditional UPDATE (WHERE status='pending') is what makes cancellation
// safe against the answer-vs-ctx.Done race documented at Route's select: a row
// an operator (or the timeout scanner) already settled must never be
// relabelled cancelled underneath them.
func TestCancelToolInputRequest_NeverClobbersAnAlreadyResolvedRow(t *testing.T) {
	s, _, _, _ := newRoutingFixture(t, time.Minute)

	requestID := model.NewULID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.CreateToolInputRequest(context.Background(), db.CreateToolInputRequestParams{
		ID:              requestID,
		RunID:           "run1",
		ServerID:        "srv1",
		ToolName:        "myserver.deploy",
		CallArgs:        "{}",
		RequestState:    "{}",
		RequestPayload:  "[]",
		ElicitationKind: "permission",
		ExpiresAt:       now,
	}); err != nil {
		t.Fatalf("CreateToolInputRequest: %v", err)
	}
	resp := "[]"
	if _, err := s.ResolveToolInputRequest(context.Background(), db.ResolveToolInputRequestParams{
		Response:   &resp,
		ResolvedAt: &now,
		ID:         requestID,
	}); err != nil {
		t.Fatalf("ResolveToolInputRequest: %v", err)
	}

	rows, err := s.CancelToolInputRequest(context.Background(), db.CancelToolInputRequestParams{ResolvedAt: &now, ID: requestID})
	if err != nil {
		t.Fatalf("CancelToolInputRequest: %v", err)
	}
	if rows != 0 {
		t.Fatalf("CancelToolInputRequest affected %d rows, want 0 — an already-resolved row must not be relabelled", rows)
	}

	row, err := s.GetToolInputRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "resolved" {
		t.Errorf("status = %q after a failed cancel attempt, want resolved", row.Status)
	}
}

// A malformed answer must leave the run paused and answerable rather than
// failing it — the validation runs on the resolving caller's side of the handoff.
func TestInputRequiredHandler_Resolve_RejectsMalformedAnswers(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "not_json", body: `not json`, want: "does not parse"},
		{name: "wrong_count", body: `[]`, want: "expected 1"},
		{name: "unknown_action", body: `[{"action":"maybe"}]`, want: "unknown action"},
		{name: "accept_without_content", body: `[{"action":"accept"}]`, want: "requires content"},
		{name: "decline_with_content", body: `[{"action":"decline","content":{"x":1}}]`, want: "must not carry content"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, pub, sm, h := newRoutingFixture(t, time.Minute)

			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _, _ = h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
			}()

			requestID := awaitPendingToolInputID(t, pub, s)

			err := h.Resolve(requestID, tc.body, testResponder)
			if err == nil {
				t.Fatalf("Resolve(%s): want an error, got nil", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Resolve error = %v, want it to mention %q", err, tc.want)
			}

			// Still paused, still answerable.
			if sm.Current() != model.RunStatusWaitingForFeedback {
				t.Errorf("run status = %s, want waiting_for_feedback", sm.Current())
			}
			if err := h.Resolve(requestID, `[{"action":"decline"}]`, testResponder); err != nil {
				t.Fatalf("Resolve after a rejected answer: %v", err)
			}
			<-done
		})
	}
}

func TestInputRequiredHandler_Resolve_UnknownRequestID(t *testing.T) {
	_, _, _, h := newRoutingFixture(t, time.Minute)

	if err := h.Resolve("nope", `[{"action":"decline"}]`, testResponder); !errors.Is(err, ErrUnknownInputRequestID) {
		t.Errorf("Resolve error = %v, want ErrUnknownInputRequestID", err)
	}
	if err := h.Decline("nope"); !errors.Is(err, ErrUnknownInputRequestID) {
		t.Errorf("Decline error = %v, want ErrUnknownInputRequestID", err)
	}
}

func TestClassifyElicitationKind(t *testing.T) {
	tests := []struct {
		name     string
		requests []mcp.InputRequest
		want     model.ElicitationKind
	}{
		{
			name:     "no_fields_is_consent_only",
			requests: []mcp.InputRequest{{RequestedSchema: json.RawMessage(`{"type":"object","properties":{}}`)}},
			want:     elicitationKindPermission,
		},
		{
			name:     "absent_schema_is_consent_only",
			requests: []mcp.InputRequest{{}},
			want:     elicitationKindPermission,
		},
		{
			name:     "fields_need_a_form",
			requests: []mcp.InputRequest{{RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ticket":{"type":"string"}}}`)}},
			want:     elicitationKindInformation,
		},
		{
			name: "explicit_meta_kind_wins_over_the_convention",
			requests: []mcp.InputRequest{{
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{}}`),
				ElicitationKind: string(elicitationKindInformation),
			}},
			want: elicitationKindInformation,
		},
		{
			// A permission hint on a schema that asks for fields is honored:
			// the server knows what it is doing with its own request.
			name: "explicit_meta_kind_wins_in_the_other_direction",
			requests: []mcp.InputRequest{{
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ticket":{"type":"string"}}}`),
				ElicitationKind: string(elicitationKindPermission),
			}},
			want: elicitationKindPermission,
		},
		{
			// A hint outside the vocabulary is a server that got an optional
			// field wrong; the schema shape is still readable, so fall through
			// to the convention rather than honoring or rejecting it.
			name: "malformed_meta_kind_falls_back_to_the_convention",
			requests: []mcp.InputRequest{{
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ticket":{"type":"string"}}}`),
				ElicitationKind: "urgent",
			}},
			want: elicitationKindInformation,
		},
		{
			// Security review finding 4: permission wins. Bundling one
			// consent-only entry alongside an information entry must not let
			// the WHOLE ask be answered by an operator instead of an
			// approver — that would let a bundled permission entry hide
			// behind an unrelated information entry.
			name: "any_permission_shaped_entry_makes_the_whole_batch_permission",
			requests: []mcp.InputRequest{
				{RequestedSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
				{RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ticket":{"type":"string"}}}`)},
			},
			want: elicitationKindPermission,
		},
		{
			name: "every_entry_information_stays_information",
			requests: []mcp.InputRequest{
				{RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ticket":{"type":"string"}}}`)},
				{RequestedSchema: json.RawMessage(`{"type":"object","properties":{"region":{"type":"string"}}}`)},
			},
			want: elicitationKindInformation,
		},
		{
			name:     "unreadable_schema_is_not_treated_as_consent_only",
			requests: []mcp.InputRequest{{RequestedSchema: json.RawMessage(`{`)}},
			want:     elicitationKindInformation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyElicitationKind(tc.requests); got != tc.want {
				t.Errorf("classifyElicitationKind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutcomeFor(t *testing.T) {
	tests := []struct {
		name      string
		responses []mcp.InputResponse
		want      decision.Outcome
	}{
		{
			name:      "single_accept",
			responses: []mcp.InputResponse{{Action: inputActionAccept}},
			want:      decision.OutcomeAnswered,
		},
		{
			name:      "single_decline",
			responses: []mcp.InputResponse{{Action: inputActionDecline}},
			want:      decision.OutcomeRejected,
		},
		{
			name:      "single_cancel",
			responses: []mcp.InputResponse{{Action: inputActionCancel}},
			want:      decision.OutcomeCancelled,
		},
		{
			name:      "every_response_cancelled",
			responses: []mcp.InputResponse{{Action: inputActionCancel}, {Action: inputActionCancel}},
			want:      decision.OutcomeCancelled,
		},
		{
			// LOW-finding follow-up: a decline mixed with a cancel on another
			// entry is still a human refusal — the person who declined acted,
			// and that must not be erased by treating the bundle as an
			// unattended cancel just because a different entry rode along
			// with it.
			name:      "decline_mixed_with_cancel_is_rejected_not_cancelled",
			responses: []mcp.InputResponse{{Action: inputActionDecline}, {Action: inputActionCancel}},
			want:      decision.OutcomeRejected,
		},
		{
			// And the reverse order, to prove this isn't an artifact of scan
			// order — the FIRST entry seen must not decide the outcome.
			name:      "cancel_then_decline_is_still_rejected",
			responses: []mcp.InputResponse{{Action: inputActionCancel}, {Action: inputActionDecline}},
			want:      decision.OutcomeRejected,
		},
		{
			// An accept anywhere in the bundle wins over a decline or cancel
			// elsewhere in it — the highest-priority outcome, regardless of
			// position.
			name: "accept_wins_over_decline_and_cancel",
			responses: []mcp.InputResponse{
				{Action: inputActionDecline},
				{Action: inputActionCancel},
				{Action: inputActionAccept},
			},
			want: decision.OutcomeAnswered,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := outcomeFor(tc.responses); got != tc.want {
				t.Errorf("outcomeFor(%+v) = %q, want %q", tc.responses, got, tc.want)
			}
		})
	}
}

// mrtrServer is a tools/call server that answers the first round with
// input_required and every later round with a completed result, recording each
// request body so a test can assert what the retry carried.
type mrtrServer struct {
	mu       sync.Mutex
	requests []map[string]any
	// alwaysAsk keeps answering input_required, modelling a server that never
	// stops asking.
	alwaysAsk bool
	// secretSchema makes the elicitation ask for a secret value, which the
	// host refuses to render as a form.
	secretSchema bool

	// reAsksAfterAnswer models a server whose MRTR state expired while a human
	// was thinking (spec §6.5): it answers that many post-answer retries with a
	// FRESH input_required instead of completing the call. Each re-ask carries
	// a new requestState, because a server that still had the old one would
	// have had no reason to re-ask.
	reAsksAfterAnswer int
	// reAskMessage overrides the message on those re-asks. Empty means the
	// server re-asks the identical question, which is the replay path; a
	// different message is the re-prompt path.
	reAskMessage string
	// reAskMessageFromRound delays reAskMessage's override to round >= this
	// value (0 means "from the first re-ask", the existing behavior). This is
	// what lets a test model a server that re-asks IDENTICALLY once (round 2,
	// the replay path) and then asks something DIFFERENT the round after
	// (round 3) -- both within the same reAsksAfterAnswer run.
	reAskMessageFromRound int
	// informationSchema makes requestedSchema ask for a field, classifying the
	// bundle as "information" instead of the default "permission" -- security
	// review findings 1-3 make permission asks never replay, so the replay
	// tests need an information-shaped bundle to exercise that path at all.
	informationSchema bool
	// reAskKindHint, when set, is sent as params._meta["io.gleipnir/elicitation-kind"]
	// on every re-ask round (round > 1) only -- byte-identical message and
	// schema, but a flipped classification hint, for the "kind changed under
	// an unchanged fingerprint" case (security review findings 1-3).
	reAskKindHint string

	// asks counts the input_required responses emitted so far.
	asks int
}

func (m *mrtrServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		m.mu.Lock()
		m.requests = append(m.requests, req)
		round := len(m.requests)
		ask := m.alwaysAsk || round == 1 || m.asks <= m.reAsksAfterAnswer
		message := "deploy to prod?"
		if ask {
			switch {
			case round == 1:
				// The opening question.
			case m.reAskMessage != "" && round >= m.reAskMessageFromRound:
				message = m.reAskMessage
			case m.alwaysAsk:
				// A server that never stops asking asks something NEW each
				// time. An endless repeat of one question is the §6.5 replay
				// case, which is a different scenario with its own tests —
				// keeping them apart is what lets each assert a clean count.
				message = fmt.Sprintf("deploy to prod? (round %d)", m.asks+1)
			}
			m.asks++
		}
		asks := m.asks
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if ask {
			// The id shifts every ask ("q1", "q2", ...) even when the message
			// and schema are byte-identical -- a server MAY legitimately mint
			// a fresh id for an identical re-ask (ADR-061), and this is what
			// lets the replay tests prove re-keying onto the NEW round's id
			// rather than assuming the old one still names anything.
			id := fmt.Sprintf("q%d", asks)
			params := map[string]any{
				"message":         message,
				"requestedSchema": m.requestedSchema(),
			}
			if round > 1 && m.reAskKindHint != "" {
				params["_meta"] = map[string]any{"io.gleipnir/elicitation-kind": m.reAskKindHint}
			}
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]any{
					"content":    json.RawMessage(`[{"type":"text","text":"pending"}]`),
					"resultType": mcp.ResultTypeInputRequired,
					// ADR-061: map keyed by request id, per go-sdk v1.7.0.
					"inputRequests": map[string]any{
						id: map[string]any{
							"method": "elicitation/create",
							"params": params,
						},
					},
					// A distinct cursor per ask, so a test can tell which
					// requestState the retry carried back.
					"requestState": map[string]any{"cursor": fmt.Sprintf("abc-%d", asks)},
				},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": json.RawMessage(`[{"type":"text","text":"deployed"}]`),
				"isError": false,
			},
		})
	}
}

// requestedSchema returns the schema the fake asks with: consent-only by
// default, a secret-collecting form when secretSchema is set, or an ordinary
// information form (one field) when informationSchema is set.
func (m *mrtrServer) requestedSchema() map[string]any {
	switch {
	case m.secretSchema:
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"api_token": map[string]any{"type": "string", "format": "password"},
			},
		}
	case m.informationSchema:
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ticket": map[string]any{"type": "string"},
			},
		}
	default:
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
}

// params returns the params object of the recorded request at index i.
func (m *mrtrServer) params(t *testing.T, i int) map[string]any {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if i >= len(m.requests) {
		t.Fatalf("no recorded request at index %d (have %d)", i, len(m.requests))
	}
	params, ok := m.requests[i]["params"].(map[string]any)
	if !ok {
		t.Fatalf("request %d has no params object: %v", i, m.requests[i])
	}
	return params
}

func (m *mrtrServer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// mrtrTool builds a modern-pinned ResolvedTool: MRTR retries only ride the
// 2026-07-28 transport, so a legacy client would silently drop inputResponses.
func mrtrTool(serverURL, serverID string, approval model.ApprovalMode) mcp.ResolvedTool {
	return mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: "myserver",
			ToolName:   "deploy",
			Approval:   approval,
		},
		// A permissive elicitation rate limit: these tests exercise the pause
		// and retry machinery, and the default bucket (spec §6.2 cap 3) would
		// otherwise refuse the flood a round-limit test deliberately creates.
		// TestBoundAgent_ToolCall_ElicitationRateLimit covers the cap itself.
		Client: mcp.NewClient(serverURL,
			mcp.WithProtocolVersion(mcp.ProtocolVersion20260728),
			mcp.WithElicitationRateLimit(1000, 1000),
		),
		ServerID:    serverID,
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"env":{"type":"string"}}}`),
	}
}

// newMRTRAgent wires a BoundAgent over an MRTR server. Returns the store, the
// publisher, the agent, and the fake server for assertions.
func newMRTRAgent(t *testing.T, fake *mrtrServer, approval model.ApprovalMode, approvalCh <-chan bool) (*db.Store, *capturePublisher, *BoundAgent) {
	t.Helper()
	s := testutil.NewTestStore(t)
	insertTestResponderUser(t, s)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)
	testutil.InsertMcpServer(t, s, "srv1", "myserver", "http://example.invalid")

	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	pub := &capturePublisher{}
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { _ = w.Close() })

	ba, err := New(Config{
		LLMClient:    testutil.NewMockLLMClient(),
		Tools:        []mcp.ResolvedTool{mrtrTool(srv.URL, "srv1", approval)},
		Policy:       minimalPolicy(),
		Audit:        w,
		ApprovalCh:   approvalCh,
		StateMachine: NewRunStateMachine("r1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, pub, ba
}

// stepTypes returns the run's step types in order.
func stepTypes(t *testing.T, s *db.Store, w *AuditWriter, runID string) []string {
	t.Helper()
	if err := w.Close(); err != nil {
		t.Fatalf("audit Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	types := make([]string, len(steps))
	for i, step := range steps {
		types[i] = step.Type
	}
	return types
}

// The whole point of the feature: the pause happens, the call is retried with
// the operator's answer, and the agent's trace shows one tool_call and one
// tool_result with no MRTR plumbing in between.
func TestBoundAgent_ToolCall_PausesAndRetriesWithAnswer(t *testing.T) {
	fake := &mrtrServer{}
	s, pub, ba := newMRTRAgent(t, fake, model.ApprovalModeNone, nil)

	type callResult struct {
		output  string
		isError bool
		err     error
	}
	done := make(chan callResult, 1)
	go func() {
		output, isError, err := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
		done <- callResult{output: output, isError: isError, err: err}
	}()

	requestID := awaitPendingToolInputID(t, pub, s)
	if err := ba.InputRequiredResolver().Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("handleToolCall: unexpected error: %v", res.err)
		}
		if res.isError {
			t.Errorf("isError = true, want false")
		}
		if !strings.Contains(res.output, "deployed") {
			t.Errorf("output = %q, want the retry's result", res.output)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handleToolCall did not return within deadline")
	}

	if fake.count() != 2 {
		t.Fatalf("server saw %d calls, want 2 (original + retry)", fake.count())
	}

	// The retry must be the same call, carrying the answer and the server's
	// own requestState back verbatim.
	retry := fake.params(t, 1)
	if name, _ := retry["name"].(string); name != "deploy" {
		t.Errorf("retry called %q, want the original tool", name)
	}
	args, _ := retry["arguments"].(map[string]any)
	if env, _ := args["env"].(string); env != "prod" {
		t.Errorf("retry arguments = %v, want the original arguments", args)
	}
	responses, ok := retry["inputResponses"].(map[string]any)
	if !ok || len(responses) != 1 {
		t.Fatalf("retry inputResponses = %v, want one entry", retry["inputResponses"])
	}
	entry, _ := responses["q1"].(map[string]any)
	if entry == nil {
		t.Fatalf("retry inputResponses = %v, want an entry keyed \"q1\"", responses)
	}
	if action, _ := entry["action"].(string); action != inputActionAccept {
		t.Errorf("retry action = %v, want accept", entry)
	}
	// ADR-061: the responder is asserted in the response's own _meta.
	meta, _ := entry["_meta"].(map[string]any)
	responder, _ := meta["io.gleipnir/responder"].(map[string]any)
	if responder["username"] != testResponder.Username || responder["user_id"] != testResponder.UserID {
		t.Errorf("retry responder = %v, want %+v", responder, testResponder)
	}
	state, ok := retry["requestState"].(map[string]any)
	if !ok || state["cursor"] != "abc-1" {
		t.Errorf("retry requestState = %v, want the server's blob replayed", retry["requestState"])
	}

	// ADR-046: the agent-visible trace shows the call and its result, nothing else.
	types := stepTypes(t, s, ba.audit, "r1")
	want := []string{string(model.StepTypeToolCall), string(model.StepTypeToolResult)}
	if len(types) != len(want) || types[0] != want[0] || types[1] != want[1] {
		t.Errorf("run steps = %v, want exactly %v", types, want)
	}
}

// Both gates can fire on one call. The ADR-008 gate runs first, pre-execution;
// the tool's own ask comes later, mid-execution. The trace shows that order.
func TestBoundAgent_ToolCall_ApprovalThenToolInitiatedInput(t *testing.T) {
	approvalCh := make(chan bool, 1)
	approvalCh <- true

	fake := &mrtrServer{}
	s, pub, ba := newMRTRAgent(t, fake, model.ApprovalModeRequired, approvalCh)

	done := make(chan error, 1)
	go func() {
		_, _, err := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
		done <- err
	}()

	// The approval lands first (the channel is pre-loaded), so the first
	// waiting_for_feedback pause is the tool-initiated one.
	requestID := awaitPendingToolInputID(t, pub, s)
	if err := ba.InputRequiredResolver().Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleToolCall: unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handleToolCall did not return within deadline")
	}

	types := stepTypes(t, s, ba.audit, "r1")
	want := []string{
		string(model.StepTypeApprovalRequest),
		string(model.StepTypeToolCall),
		string(model.StepTypeToolResult),
	}
	if len(types) != len(want) {
		t.Fatalf("run steps = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("run steps = %v, want %v", types, want)
		}
	}
}

// A server that never stops asking must not pause a run forever. The call is
// abandoned; the run is not, so the agent gets a correctable tool_result.
func TestBoundAgent_ToolCall_InputRoundLimit(t *testing.T) {
	fake := &mrtrServer{alwaysAsk: true}
	s, pub, ba := newMRTRAgent(t, fake, model.ApprovalModeNone, nil)

	type callResult struct {
		output  string
		isError bool
		err     error
	}
	done := make(chan callResult, 1)
	go func() {
		output, isError, err := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
		done <- callResult{output: output, isError: isError, err: err}
	}()

	// Answer every pause immediately; the limit, not the operator, ends this.
	for n := 1; n < maxInputRequiredRounds; n++ {
		requestID := awaitNthPendingToolInputID(t, pub, s, n)
		if err := ba.InputRequiredResolver().Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
			t.Fatalf("Resolve #%d: %v", n, err)
		}
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("handleToolCall: unexpected error: %v", res.err)
		}
		if !res.isError {
			t.Error("isError = false, want true — the call was abandoned")
		}
		if !strings.Contains(res.output, "asked for operator input") {
			t.Errorf("output = %q, want the round-limit explanation", res.output)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("handleToolCall did not return within deadline")
	}

	if fake.count() != maxInputRequiredRounds {
		t.Errorf("server saw %d calls, want %d", fake.count(), maxInputRequiredRounds)
	}
}

// An unanswered pause fails the run: there is nothing for the agent to correct
// toward, and the run belongs in the operator attention queue.
func TestBoundAgent_ToolCall_InputTimeoutFailsTheRun(t *testing.T) {
	fake := &mrtrServer{alwaysAsk: true}
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)
	testutil.InsertMcpServer(t, s, "srv1", "myserver", "http://example.invalid")

	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { _ = w.Close() })

	policy := minimalPolicy()
	// The policy clock is the human leg's authority (spec §6.3); 10ms is that
	// clock set deliberately short, not a race against another actor.
	policy.Capabilities.Feedback = model.FeedbackConfig{Timeout: "10ms"}

	ba, err := New(Config{
		LLMClient:    testutil.NewMockLLMClient(),
		Tools:        []mcp.ResolvedTool{mrtrTool(srv.URL, "srv1", model.ApprovalModeNone)},
		Policy:       policy,
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusRunning, s.DB(), s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _, callErr := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
	if callErr == nil {
		t.Fatal("handleToolCall: want an error, got nil")
	}
	var routeErr *InputRoutingError
	if !errors.As(callErr, &routeErr) {
		t.Fatalf("handleToolCall error = %v, want *InputRoutingError", callErr)
	}
}

// The budget hook is called before any routing happens, and a refusal abandons
// the call without pausing the run. Enforcement itself lands separately; this
// pins the call site.
func TestInputRequiredHandler_Route_BudgetRefusalSkipsThePause(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)
	testutil.InsertMcpServer(t, s, "srv1", "myserver", "http://example.invalid")

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { _ = w.Close() })

	budget := &recordingBudget{err: errors.New("per-run elicitation budget exhausted")}
	h := NewInputRequiredHandler(w, sm, time.Minute, WithElicitationBudget(budget))

	_, _, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
	if err == nil {
		t.Fatal("Route: want the budget refusal, got nil")
	}
	if !strings.Contains(err.Error(), "elicitation budget") {
		t.Errorf("Route error = %v, want the budget refusal", err)
	}
	if budget.runID != "run1" || budget.requests != 1 {
		t.Errorf("budget called with (%q, %d), want (run1, 1)", budget.runID, budget.requests)
	}
	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running — a refused pause never suspends the run", sm.Current())
	}
	rows, err := s.ListResumableToolInputRequests(context.Background())
	if err != nil {
		t.Fatalf("ListResumableToolInputRequests: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d rows persisted, want 0", len(rows))
	}
}

type recordingBudget struct {
	err      error
	runID    string
	requests int
}

func (b *recordingBudget) Check(_ context.Context, runID string, requests int) error {
	b.runID = runID
	b.requests = requests
	return b.err
}

// resolveOperatorTimeout is shared by both operator waits; the policy clock
// wins when it parses, and a corrupt value falls back rather than propagating.
func TestResolveOperatorTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  model.FeedbackConfig
		def  time.Duration
		want time.Duration
	}{
		{name: "policy_value_wins", cfg: model.FeedbackConfig{Timeout: "5m"}, def: time.Minute, want: 5 * time.Minute},
		{name: "empty_falls_back", cfg: model.FeedbackConfig{}, def: time.Minute, want: time.Minute},
		{name: "unparseable_falls_back", cfg: model.FeedbackConfig{Timeout: "banana"}, def: time.Minute, want: time.Minute},
		{name: "zero_default_stays_zero", cfg: model.FeedbackConfig{}, def: 0, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveOperatorTimeout(context.Background(), tc.cfg, tc.def); got != tc.want {
				t.Errorf("resolveOperatorTimeout = %s, want %s", got, tc.want)
			}
		})
	}
}

// Compile-time proof that the handler's LLM dependency is unused: the MRTR
// pause never reaches the model. NewMockLLMClient is only wired above so a
// future edit that does reach it fails loudly rather than nil-panicking.
var _ llm.LLMClient = testutil.NewMockLLMClient()

// Spec §6.1: form mode never carries secrets. URL mode exists for that and
// does not exist yet, so a server that asks for one gets a refusal — not a
// redacted form, which would leave the operator answering a question they
// cannot see.
func TestCheckNoSecretFields(t *testing.T) {
	tests := []struct {
		name      string
		schema    string
		wantError bool
	}{
		{name: "ordinary_field", schema: `{"type":"object","properties":{"ticket":{"type":"string"}}}`},
		{name: "no_schema", schema: ``},
		{
			name:      "gleipnir_secret_marker",
			schema:    `{"type":"object","properties":{"token":{"type":"string","x-gleipnir-secret":true}}}`,
			wantError: true,
		},
		{
			name:      "password_format",
			schema:    `{"type":"object","properties":{"pw":{"type":"string","format":"password"}}}`,
			wantError: true,
		},
		{
			name:      "write_only",
			schema:    `{"type":"object","properties":{"pw":{"type":"string","writeOnly":true}}}`,
			wantError: true,
		},
		{
			// Whitespace between the key and its value must not smuggle a
			// marker past the check.
			name:      "password_format_with_whitespace",
			schema:    "{\"type\":\"object\",\"properties\":{\"pw\":{\n  \"format\" : \"password\"\n}}}",
			wantError: true,
		},
		{
			// Nesting depth is not a hiding place either.
			name:      "marker_nested_deep",
			schema:    `{"type":"object","properties":{"outer":{"type":"object","properties":{"inner":{"format":"password"}}}}}`,
			wantError: true,
		},
		{
			// A description that merely talks about passwords is not a secret
			// field: whitespace inside strings is preserved, so the marker
			// pattern cannot form by accident.
			name:   "description_mentioning_password",
			schema: `{"type":"object","properties":{"note":{"type":"string","description":"do not paste your format: password here"}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := []mcp.InputRequest{{RequestedSchema: json.RawMessage(tc.schema)}}
			err := checkNoSecretFields(requests)
			if tc.wantError {
				if !errors.Is(err, ErrSecretElicitation) {
					t.Fatalf("checkNoSecretFields = %v, want ErrSecretElicitation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkNoSecretFields = %v, want nil", err)
			}
		})
	}
}

// The refusal happens before the pause: no row is persisted, the run is never
// suspended, and the agent gets a correctable tool_result instead.
func TestBoundAgent_ToolCall_SecretElicitationIsRefusedBeforePausing(t *testing.T) {
	fake := &mrtrServer{alwaysAsk: true, secretSchema: true}
	s, pub, ba := newMRTRAgent(t, fake, model.ApprovalModeNone, nil)

	output, isError, err := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
	if err != nil {
		t.Fatalf("handleToolCall: unexpected error: %v", err)
	}
	if !isError {
		t.Error("isError = false, want true — the call was abandoned")
	}
	if !strings.Contains(output, "secret") {
		t.Errorf("output = %q, want the refusal explanation", output)
	}
	if fake.count() != 1 {
		t.Errorf("server saw %d calls, want 1 — the refusal must not retry", fake.count())
	}
	if n := pub.countByType("tool_input.created"); n != 0 {
		t.Errorf("%d pauses were published, want 0 — a refused elicitation never reaches an operator", n)
	}

	rows, err := s.ListResumableToolInputRequests(context.Background())
	if err != nil {
		t.Fatalf("ListResumableToolInputRequests: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d rows persisted, want 0", len(rows))
	}
}

// The budget is fail-closed: once spent it refuses unconditionally, and a
// request that would overrun it consumes nothing rather than partially
// spending — a partial spend would let a smaller later ask still succeed,
// which reads as the cap being negotiable.
func TestRunElicitationBudget(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		asks    []int
		wantErr []bool
		want    int
	}{
		{
			name:    "single asks up to the limit",
			limit:   3,
			asks:    []int{1, 1, 1, 1},
			wantErr: []bool{false, false, false, true},
			want:    3,
		},
		{
			name:    "a batch consumes its whole size",
			limit:   5,
			asks:    []int{3, 2, 1},
			wantErr: []bool{false, false, true},
			want:    5,
		},
		{
			name:    "an overrunning ask spends nothing",
			limit:   3,
			asks:    []int{2, 3, 1},
			wantErr: []bool{false, true, false},
			want:    3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newRunElicitationBudget(tc.limit)
			for i, ask := range tc.asks {
				err := b.Check(context.Background(), "run1", ask)
				if gotErr := err != nil; gotErr != tc.wantErr[i] {
					t.Fatalf("ask %d (%d requests): err = %v, want error = %v", i, ask, err, tc.wantErr[i])
				}
				if err != nil && !errors.Is(err, ErrElicitationBudgetExhausted) {
					t.Errorf("ask %d: error %v does not wrap ErrElicitationBudgetExhausted", i, err)
				}
			}
			if got := b.spentCount(); got != tc.want {
				t.Errorf("spent = %d, want %d", got, tc.want)
			}
		})
	}
}

// A zero or negative limit means unlimited, and carries no counter at all.
func TestNewRunElicitationBudget_UnlimitedIsNil(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if b := newRunElicitationBudget(limit); b != nil {
			t.Errorf("newRunElicitationBudget(%d) = %v, want nil (unlimited)", limit, b)
		}
	}
}

// A policy's max_elicitations_per_run reaches the handler without any wiring
// at the call site.
func TestInputRequiredOptions_FromPolicy(t *testing.T) {
	if got := inputRequiredOptions(nil); got != nil {
		t.Error("a nil policy produced options")
	}

	unlimited := minimalPolicy()
	if got := inputRequiredOptions(unlimited); got != nil {
		t.Error("a policy with no elicitation limit produced a budget")
	}

	limited := minimalPolicy()
	limited.Agent.Limits.MaxElicitationsPerRun = 2
	if got := inputRequiredOptions(limited); len(got) != 1 {
		t.Fatalf("options = %d, want 1 for a policy with a limit", len(got))
	}
}

// The N+1th elicitation fails the CALL structurally and lets the run continue
// (spec §6.2) — nobody was interrupted, so there is no abandoned operator wait.
func TestBoundAgent_ToolCall_BudgetExhaustionAbandonsTheCall(t *testing.T) {
	fake := &mrtrServer{alwaysAsk: true}
	s := testutil.NewTestStore(t)
	insertTestResponderUser(t, s)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)
	testutil.InsertMcpServer(t, s, "srv1", "myserver", "http://example.invalid")

	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	pub := &capturePublisher{}
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { _ = w.Close() })

	policy := minimalPolicy()
	policy.Agent.Limits.MaxElicitationsPerRun = 2

	ba, err := New(Config{
		LLMClient:    testutil.NewMockLLMClient(),
		Tools:        []mcp.ResolvedTool{mrtrTool(srv.URL, "srv1", model.ApprovalModeNone)},
		Policy:       policy,
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	var output string
	var isError bool
	var callErr error
	go func() {
		defer close(done)
		output, isError, callErr = ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
	}()

	// Answer the two pauses the budget allows; the third is refused before it
	// ever reaches an operator.
	for n := 1; n <= 2; n++ {
		requestID := awaitNthPendingToolInputID(t, pub, s, n)
		if err := ba.InputRequiredResolver().Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
			t.Fatalf("Resolve #%d: %v", n, err)
		}
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handleToolCall did not return within deadline")
	}

	if callErr != nil {
		t.Fatalf("handleToolCall: the run must continue: %v", callErr)
	}
	if !isError {
		t.Error("isError = false, want true — the call was abandoned")
	}
	if !strings.Contains(output, "budget") {
		t.Errorf("output = %q, want the budget explanation", output)
	}
	if n := pub.countByType("tool_input.created"); n != 2 {
		t.Errorf("%d pauses reached an operator, want 2 — the third must be refused before routing", n)
	}
}

// A pause whose durable record would be unreasonably large is refused before
// any row is written. internal/mcp caps this at decode time; this is the
// persistence-layer backstop for any producer that does not come through it.
func TestInputRequiredHandler_Route_OversizePayloadIsRefusedBeforePersisting(t *testing.T) {
	tests := []struct {
		name   string
		result *mcp.InputRequiredResult
	}{
		{
			name: "oversize requestState",
			result: &mcp.InputRequiredResult{
				InputRequests: []mcp.InputRequest{{Message: "ok"}},
				RequestState:  json.RawMessage(strings.Repeat("x", maxPersistedRequestStateBytes+1)),
			},
		},
		{
			name: "oversize elicitation payload",
			result: &mcp.InputRequiredResult{
				InputRequests: []mcp.InputRequest{{Message: strings.Repeat("x", maxPersistedPayloadBytes+1)}},
				RequestState:  json.RawMessage(`{"cursor":"abc"}`),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, sm, h := newRoutingFixture(t, time.Minute)

			_, _, err := h.Route(context.Background(), routeRequest(tc.result, time.Minute))
			if err == nil {
				t.Fatal("Route: want a size refusal, got nil")
			}
			if sm.Current() != model.RunStatusRunning {
				t.Errorf("run status = %s, want running — a refused pause never suspends the run", sm.Current())
			}
			rows, listErr := s.ListResumableToolInputRequests(context.Background())
			if listErr != nil {
				t.Fatalf("ListResumableToolInputRequests: %v", listErr)
			}
			if len(rows) != 0 {
				t.Errorf("%d rows persisted, want 0", len(rows))
			}
		})
	}
}

// The effective deadline reaches the row, and so does the clock that produced
// it — that pairing is what lets a later failure explain itself.
func TestInputRequiredHandler_Route_PersistsTheEffectiveDeadline(t *testing.T) {
	tests := []struct {
		name       string
		serverTTL  time.Duration // relative to now; zero means no server clock
		timeout    time.Duration
		wantSource DeadlineSource
	}{
		{
			name:       "policy clock governs when no server clock applies",
			timeout:    time.Hour,
			wantSource: DeadlineSourcePolicy,
		},
		{
			name:       "a shorter server TTL governs",
			serverTTL:  5 * time.Minute,
			timeout:    time.Hour,
			wantSource: DeadlineSourceServerTTL,
		},
		{
			name:       "a longer server TTL does not extend the policy clock",
			serverTTL:  2 * time.Hour,
			timeout:    time.Minute,
			wantSource: DeadlineSourcePolicy,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, pub, _, h := newRoutingFixture(t, time.Minute)

			req := routeRequest(permissionAsk("deploy to prod?"), tc.timeout)
			if tc.serverTTL != 0 {
				req.ServerTaskTTL = time.Now().UTC().Add(tc.serverTTL)
			}

			go func() { _, _, _ = h.Route(context.Background(), req) }()
			requestID := awaitPendingToolInputID(t, pub, s)

			row, err := s.GetToolInputRequest(context.Background(), requestID)
			if err != nil {
				t.Fatalf("GetToolInputRequest: %v", err)
			}
			if row.DeadlineSource == nil {
				t.Fatal("deadline_source is NULL; a freshly written pause always knows its clock")
			}
			if *row.DeadlineSource != string(tc.wantSource) {
				t.Errorf("deadline_source = %q, want %q", *row.DeadlineSource, tc.wantSource)
			}

			deadline, err := time.Parse(time.RFC3339Nano, row.ExpiresAt)
			if err != nil {
				t.Fatalf("expires_at %q does not parse: %v", row.ExpiresAt, err)
			}
			if tc.wantSource == DeadlineSourceServerTTL && time.Until(deadline) > tc.timeout {
				t.Errorf("effective deadline %s is past the policy timeout; the server clock did not shorten it", row.ExpiresAt)
			}

			// Release the goroutine.
			_ = h.Decline(requestID)
		})
	}
}

// A server TTL already in the past ends the wait immediately rather than
// hanging for the full policy timeout — and records that the server ended it.
func TestInputRequiredHandler_Route_ExpiredServerTTLEndsTheWaitAtOnce(t *testing.T) {
	_, _, _, h := newRoutingFixture(t, time.Hour)

	req := routeRequest(permissionAsk("deploy to prod?"), time.Hour)
	req.ServerTaskTTL = time.Now().UTC().Add(-time.Hour)

	done := make(chan error, 1)
	go func() {
		_, _, err := h.Route(context.Background(), req)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Route: want a timeout error for an already-expired server TTL")
		}
		if !strings.Contains(err.Error(), "the server, not Gleipnir, ended this wait") {
			t.Errorf("error = %v, want it to name the server as the cause", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Route waited on the policy clock despite an expired server TTL")
	}
}

// §6.5, the case the whole mechanism exists for: the operator answers, the
// server has meanwhile discarded its MRTR state and re-asks the SAME
// INFORMATION question, and the answer is replayed against the fresh
// requestState without the human ever being asked twice. Exactly one pause
// reaches an operator. Permission asks never take this path at all (security
// review findings 1-3, see TestBoundAgent_ToolCall_PermissionAskNeverReplays)
// — this test exercises the one case that still replays.
func TestBoundAgent_ToolCall_ReplaysAnswerWhenServerReAsksIdentically(t *testing.T) {
	fake := &mrtrServer{reAsksAfterAnswer: 1, informationSchema: true}
	s, pub, ba := newMRTRAgent(t, fake, model.ApprovalModeNone, nil)

	type callResult struct {
		output  string
		isError bool
		err     error
	}
	done := make(chan callResult, 1)
	go func() {
		output, isError, err := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
		done <- callResult{output: output, isError: isError, err: err}
	}()

	requestID := awaitPendingToolInputID(t, pub, s)
	if err := ba.InputRequiredResolver().Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("handleToolCall: unexpected error: %v", res.err)
		}
		if res.isError {
			t.Errorf("isError = true, want false")
		}
		if !strings.Contains(res.output, "deployed") {
			t.Errorf("output = %q, want the completed result", res.output)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handleToolCall did not return within deadline")
	}

	// original + retry(answer) + retry(replayed answer) = 3.
	if fake.count() != 3 {
		t.Fatalf("server saw %d calls, want 3 (original + answered retry + replayed retry)", fake.count())
	}

	// The replay carries the SAME answer against the server's NEW requestState,
	// re-keyed onto round 3's OWN id ("q2") -- NOT round 1's ("q1"), which this
	// round's server never asked under. Sending back the stale blob would be
	// replaying into the same expiry the server just told us about.
	replay := fake.params(t, 2)
	responses, ok := replay["inputResponses"].(map[string]any)
	if !ok || len(responses) != 1 {
		t.Fatalf("replay inputResponses = %v, want one entry", replay["inputResponses"])
	}
	if _, stale := responses["q1"]; stale {
		t.Errorf("replay inputResponses = %v, still keyed to the ORIGINAL id, want the re-asked round's own id", responses)
	}
	entry, _ := responses["q2"].(map[string]any)
	if entry == nil {
		t.Fatalf("replay inputResponses = %v, want an entry keyed to round 3's own id \"q2\"", responses)
	}
	if action, _ := entry["action"].(string); action != inputActionAccept {
		t.Errorf("replay action = %v, want the stored answer replayed", entry)
	}
	// No human acted this round: the responder assertion is stripped, and
	// replaced with a provenance marker naming the ORIGINAL request instead
	// (security review findings 1-3).
	meta, _ := entry["_meta"].(map[string]any)
	if _, hasResponder := meta["io.gleipnir/responder"]; hasResponder {
		t.Errorf("replay _meta = %v, must not assert a responder — nobody answered this round", meta)
	}
	replayedFrom, _ := meta["io.gleipnir/replayed-from"].(map[string]any)
	if replayedFrom["request_id"] != requestID {
		t.Errorf("replayed-from = %v, want it to name the original request %q", replayedFrom, requestID)
	}
	state, _ := replay["requestState"].(map[string]any)
	if state["cursor"] != "abc-2" {
		t.Errorf("replay requestState = %v, want the server's SECOND blob", replay["requestState"])
	}

	// Exactly one row, because exactly one human was asked.
	rows, err := s.Queries().ListResumableToolInputRequests(context.Background())
	if err != nil {
		t.Fatalf("ListResumableToolInputRequests: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d requests still pending, want 0", len(rows))
	}
	all, err := s.Queries().GetToolInputRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if all.Status != "resolved" {
		t.Errorf("request status = %q, want resolved", all.Status)
	}

	// The replay itself is a distinct decision record from the original
	// answer: an auditor sees two settlement events for one human decision,
	// the second with no actor -- nobody acted a second time -- and pointing
	// back at the original request via ReplayOfRequestID.
	records, err := decision.NewRecorder(s.Queries()).ForRun(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	var sawAnswered, sawReplayed bool
	for _, rec := range records {
		switch rec.Outcome {
		case decision.OutcomeAnswered:
			sawAnswered = true
			if rec.ActorUserID != testResponder.UserID {
				t.Errorf("answered record ActorUserID = %q, want %q", rec.ActorUserID, testResponder.UserID)
			}
		case decision.OutcomeReplayedAfterTTL:
			sawReplayed = true
			if rec.ActorUserID != "" {
				t.Errorf("replayed record ActorUserID = %q, want none — nobody acted at this moment", rec.ActorUserID)
			}
			if rec.Kind != model.ElicitationKindInformation {
				t.Errorf("replayed record Kind = %q, want %q (the NEW bundle's kind)", rec.Kind, model.ElicitationKindInformation)
			}
			if rec.ReplayOfRequestID != requestID {
				t.Errorf("replayed record ReplayOfRequestID = %q, want the original request %q", rec.ReplayOfRequestID, requestID)
			}
		}
	}
	if !sawAnswered || !sawReplayed {
		t.Fatalf("decision records = %+v, want one answered and one replayed_after_ttl", records)
	}

	// And the agent still sees one call and one result — a replay is no more
	// visible to the model than the pause it recovers from (ADR-046).
	types := stepTypes(t, s, ba.audit, "r1")
	want := []string{string(model.StepTypeToolCall), string(model.StepTypeToolResult)}
	if len(types) != len(want) {
		t.Fatalf("run steps = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("run steps = %v, want %v", types, want)
		}
	}
}

// Security review findings 1-3: a permission ask NEVER replays, even when the
// server re-asks byte-for-byte the same question. Consent is not something
// the host may re-assert on a human's behalf; every permission re-ask reaches
// a human.
func TestBoundAgent_ToolCall_PermissionAskNeverReplays(t *testing.T) {
	fake := &mrtrServer{reAsksAfterAnswer: 1} // default schema is permission-shaped
	s, pub, ba := newMRTRAgent(t, fake, model.ApprovalModeNone, nil)

	type callResult struct {
		output  string
		isError bool
		err     error
	}
	done := make(chan callResult, 1)
	go func() {
		output, isError, err := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
		done <- callResult{output: output, isError: isError, err: err}
	}()

	first := awaitPendingToolInputID(t, pub, s)
	if err := ba.InputRequiredResolver().Resolve(first, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
		t.Fatalf("Resolve first: %v", err)
	}

	// A second, ordinary human pause -- not a silent replay -- even though the
	// server re-asked byte-for-byte the same permission question.
	second := awaitNthPendingToolInputID(t, pub, s, 2)
	if second == first {
		t.Fatal("the second pause reused the first request ID; want a fresh human ask, not a replay")
	}

	// LOW-finding follow-up: an identical re-ask that is NOT silently
	// replayed (a permission ask never is) still gets a ReplayContext, tagged
	// reasonIdenticalReask -- the operator needs to be told plainly that they
	// answered this exact question moments ago, not just shown what looks
	// like an unexplained duplicate.
	row, err := s.Queries().GetToolInputRequest(context.Background(), second)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.ReplayContext == nil {
		t.Fatal("second request has no replay_context; an identical permission re-ask must still explain itself")
	}
	rc, err := DecodeReplayContext(*row.ReplayContext)
	if err != nil {
		t.Fatalf("DecodeReplayContext: %v", err)
	}
	if rc.Reason != reasonIdenticalReask {
		t.Errorf("Reason = %q, want %q", rc.Reason, reasonIdenticalReask)
	}
	if len(rc.PriorAnswers) != 1 || rc.PriorAnswers[0].Action != inputActionAccept {
		t.Errorf("prior answers = %+v, want the operator's first answer preserved", rc.PriorAnswers)
	}

	if err := ba.InputRequiredResolver().Resolve(second, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
		t.Fatalf("Resolve second: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("handleToolCall: unexpected error: %v", res.err)
		}
		if !strings.Contains(res.output, "deployed") {
			t.Errorf("output = %q, want the completed result", res.output)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handleToolCall did not return within deadline")
	}

	// Two independent decision records, both answered by a human -- no
	// replayed_after_ttl outcome at all.
	records, err := decision.NewRecorder(s.Queries()).ForRun(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	answeredCount := 0
	for _, rec := range records {
		if rec.Outcome == decision.OutcomeReplayedAfterTTL {
			t.Errorf("got a replayed_after_ttl record for a permission ask, want none: %+v", rec)
		}
		if rec.Outcome == decision.OutcomeAnswered {
			answeredCount++
			if rec.ActorUserID != testResponder.UserID {
				t.Errorf("answered record ActorUserID = %q, want %q", rec.ActorUserID, testResponder.UserID)
			}
		}
	}
	if answeredCount != 2 {
		t.Fatalf("answered decision records = %d, want 2 (one per human ask)", answeredCount)
	}
}

// Security review findings 1-3: a server that re-asks the identical message
// and schema but flips the elicitation-kind hint from information to
// permission must not have its answer silently replayed under the wrong
// gate — the fingerprint alone is not enough; the kind must match on both
// sides too. The operator sees it as a fresh, differently-classified ask.
func TestBoundAgent_ToolCall_KindChangeUnderAnUnchangedFingerprintGoesToAHuman(t *testing.T) {
	fake := &mrtrServer{reAsksAfterAnswer: 1, informationSchema: true, reAskKindHint: "permission"}
	s, pub, ba := newMRTRAgent(t, fake, model.ApprovalModeNone, nil)

	done := make(chan error, 1)
	go func() {
		_, _, err := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
		done <- err
	}()

	first := awaitPendingToolInputID(t, pub, s)
	if err := ba.InputRequiredResolver().Resolve(first, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
		t.Fatalf("Resolve first: %v", err)
	}

	// The re-ask reaches a human as a SECOND pause despite matching content,
	// because the kind hint flipped it to permission.
	second := awaitNthPendingToolInputID(t, pub, s, 2)
	if second == first {
		t.Fatal("the second pause reused the first request ID; want a fresh human ask")
	}
	row, err := s.Queries().GetToolInputRequest(context.Background(), second)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.ElicitationKind != string(model.ElicitationKindPermission) {
		t.Errorf("second pause elicitation_kind = %q, want permission", row.ElicitationKind)
	}

	if err := ba.InputRequiredResolver().Resolve(second, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
		t.Fatalf("Resolve second: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleToolCall: unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handleToolCall did not return within deadline")
	}
}

// The other half of §6.5: the server re-asks something DIFFERENT, so the
// operator is prompted again — and the second prompt carries the previous
// question and answer, so it does not read as a duplicate of the first.
func TestBoundAgent_ToolCall_ReAskingDifferentlyRePromptsWithContext(t *testing.T) {
	fake := &mrtrServer{reAsksAfterAnswer: 1, reAskMessage: "deploy to prod AND restart workers?"}
	s, pub, ba := newMRTRAgent(t, fake, model.ApprovalModeNone, nil)

	type callResult struct {
		output  string
		isError bool
		err     error
	}
	done := make(chan callResult, 1)
	go func() {
		output, isError, err := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
		done <- callResult{output: output, isError: isError, err: err}
	}()

	first := awaitPendingToolInputID(t, pub, s)
	if err := ba.InputRequiredResolver().Resolve(first, `[{"action":"accept","content":{"confirm":true}}]`, testResponder); err != nil {
		t.Fatalf("Resolve first: %v", err)
	}

	second := awaitNthPendingToolInputID(t, pub, s, 2)
	if second == first {
		t.Fatal("the second prompt reused the first request ID")
	}

	// The replay context is what tells the operator the question changed.
	row, err := s.Queries().GetToolInputRequest(context.Background(), second)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.ReplayContext == nil {
		t.Fatal("second request has no replay_context; the operator cannot tell the question changed")
	}
	rc, err := DecodeReplayContext(*row.ReplayContext)
	if err != nil {
		t.Fatalf("DecodeReplayContext: %v", err)
	}
	if len(rc.PriorQuestions) != 1 || rc.PriorQuestions[0].Message != "deploy to prod?" {
		t.Errorf("prior questions = %+v, want the original ask", rc.PriorQuestions)
	}
	if len(rc.PriorAnswers) != 1 || rc.PriorAnswers[0].Action != inputActionAccept {
		t.Errorf("prior answers = %+v, want the operator's first answer preserved", rc.PriorAnswers)
	}
	if rc.Reason != reasonQuestionChanged {
		t.Errorf("Reason = %q, want %q", rc.Reason, reasonQuestionChanged)
	}

	// The first ask, by contrast, has nothing before it.
	firstRow, err := s.Queries().GetToolInputRequest(context.Background(), first)
	if err != nil {
		t.Fatalf("GetToolInputRequest(first): %v", err)
	}
	if firstRow.ReplayContext != nil {
		t.Errorf("first request carries a replay context %q, want NULL", *firstRow.ReplayContext)
	}

	if err := ba.InputRequiredResolver().Resolve(second, `[{"action":"decline"}]`, testResponder); err != nil {
		t.Fatalf("Resolve second: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("handleToolCall: unexpected error: %v", res.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handleToolCall did not return within deadline")
	}
}

// LOW-finding follow-up: `!prior.replayed` used to gate the fallback
// context-attachment branch too, not just canReplay's "once per question"
// allowance. That meant a server re-asking something genuinely DIFFERENT
// right after a spent replay got no ReplayContext at all — the operator
// would see the new question with no way to tell what they'd just answered.
// The term now belongs only to canReplay; a changed question always shows
// the prior context, replay or no replay.
func TestBoundAgent_ToolCall_ChangedQuestionAfterAReplayStillShowsPriorContext(t *testing.T) {
	fake := &mrtrServer{
		reAsksAfterAnswer:     2,
		informationSchema:     true,
		reAskMessage:          "what's the ticket number, actually?",
		reAskMessageFromRound: 3, // round 2 re-asks identically (the replay); round 3 changes.
	}
	s, pub, ba := newMRTRAgent(t, fake, model.ApprovalModeNone, nil)

	done := make(chan error, 1)
	go func() {
		_, _, err := ba.handleToolCall(context.Background(), "r1", "myserver.deploy", map[string]any{"env": "prod"})
		done <- err
	}()

	first := awaitPendingToolInputID(t, pub, s)
	if err := ba.InputRequiredResolver().Resolve(first, `[{"action":"accept","content":{"ticket":"T-1"}}]`, testResponder); err != nil {
		t.Fatalf("Resolve first: %v", err)
	}

	// Round 2 re-asks the identical question and is replayed silently — no
	// second human pause for it. Round 3 changes the question, which IS a
	// second human pause, reached with the original answer already spent on
	// one replay (prior.replayed == true).
	second := awaitNthPendingToolInputID(t, pub, s, 2)
	if second == first {
		t.Fatal("the second pause reused the first request ID")
	}

	row, err := s.Queries().GetToolInputRequest(context.Background(), second)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.ReplayContext == nil {
		t.Fatal("second request has no replay_context; a changed question after a spent replay must still show the prior answer")
	}
	rc, err := DecodeReplayContext(*row.ReplayContext)
	if err != nil {
		t.Fatalf("DecodeReplayContext: %v", err)
	}
	if rc.Reason != reasonQuestionChanged {
		t.Errorf("Reason = %q, want %q", rc.Reason, reasonQuestionChanged)
	}
	if len(rc.PriorAnswers) != 1 || rc.PriorAnswers[0].Action != inputActionAccept {
		t.Errorf("prior answers = %+v, want the operator's original answer preserved", rc.PriorAnswers)
	}

	if err := ba.InputRequiredResolver().Resolve(second, `[{"action":"accept","content":{"ticket":"T-2"}}]`, testResponder); err != nil {
		t.Fatalf("Resolve second: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleToolCall: unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handleToolCall did not return within deadline")
	}

	// original + replayed retry + changed-question retry + answered retry = 4.
	if fake.count() != 4 {
		t.Fatalf("server saw %d calls, want 4 (original + replayed retry + changed-question retry + answered retry)", fake.count())
	}
}

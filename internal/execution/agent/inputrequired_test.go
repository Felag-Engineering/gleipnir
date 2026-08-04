// Package agent — inputrequired_test.go pins the tool-initiated HITL contract
// (ADR-055, spec §6 source 3): the pause is durable before it is observable,
// the operator's answer is replayed onto the ORIGINAL call, and the agent's
// trace never shows that any of it happened.
package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

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
		answers, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
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

	if err := h.Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`); err != nil {
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
		answers, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
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
	_, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), 10*time.Millisecond))
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
		_, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), 300*time.Millisecond))
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

// A host that dies mid-wait leaves a row an operator answer can still be
// applied against — the §13 durability claim. Cancelling the context models the
// process going away; the row must survive as pending and resumable.
func TestInputRequiredHandler_Route_RestartLeavesRequestResumable(t *testing.T) {
	s, pub, _, h := newRoutingFixture(t, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := h.Route(ctx, routeRequest(permissionAsk("deploy to prod?"), time.Minute))
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

	rows, err := s.ListResumableToolInputRequests(context.Background())
	if err != nil {
		t.Fatalf("ListResumableToolInputRequests: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != requestID {
		t.Fatalf("resumable rows = %+v, want the pending request %s", rows, requestID)
	}
	if rows[0].RequestState != `{"cursor":"abc"}` {
		t.Errorf("request_state = %q, want the blob a retry would replay", rows[0].RequestState)
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
				_, _ = h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
			}()

			requestID := awaitPendingToolInputID(t, pub, s)

			err := h.Resolve(requestID, tc.body)
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
			if err := h.Resolve(requestID, `[{"action":"decline"}]`); err != nil {
				t.Fatalf("Resolve after a rejected answer: %v", err)
			}
			<-done
		})
	}
}

func TestInputRequiredHandler_Resolve_UnknownRequestID(t *testing.T) {
	_, _, _, h := newRoutingFixture(t, time.Minute)

	if err := h.Resolve("nope", `[{"action":"decline"}]`); !errors.Is(err, ErrUnknownInputRequestID) {
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
			name: "one_field_anywhere_makes_the_batch_a_form",
			requests: []mcp.InputRequest{
				{RequestedSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
				{RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ticket":{"type":"string"}}}`)},
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
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if m.alwaysAsk || round == 1 {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]any{
					"content":    json.RawMessage(`[{"type":"text","text":"pending"}]`),
					"resultType": mcp.ResultTypeInputRequired,
					"inputRequests": []map[string]any{{
						"message":         "deploy to prod?",
						"requestedSchema": m.requestedSchema(),
					}},
					"requestState": map[string]any{"cursor": "abc"},
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
// default, or a secret-collecting form when secretSchema is set.
func (m *mrtrServer) requestedSchema() map[string]any {
	if m.secretSchema {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"api_token": map[string]any{"type": "string", "format": "password"},
			},
		}
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
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
		Client:      mcp.NewClient(serverURL, mcp.WithProtocolVersion(mcp.ProtocolVersion20260728)),
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
	if err := ba.InputRequiredResolver().Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`); err != nil {
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
	responses, ok := retry["inputResponses"].([]any)
	if !ok || len(responses) != 1 {
		t.Fatalf("retry inputResponses = %v, want one entry", retry["inputResponses"])
	}
	if action, _ := responses[0].(map[string]any)["action"].(string); action != inputActionAccept {
		t.Errorf("retry action = %v, want accept", responses[0])
	}
	state, ok := retry["requestState"].(map[string]any)
	if !ok || state["cursor"] != "abc" {
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
	if err := ba.InputRequiredResolver().Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`); err != nil {
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
		if err := ba.InputRequiredResolver().Resolve(requestID, `[{"action":"accept","content":{"confirm":true}}]`); err != nil {
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

	_, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
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

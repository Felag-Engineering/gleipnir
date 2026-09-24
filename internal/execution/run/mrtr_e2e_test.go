// mrtr_e2e_test.go drives Gleipnir's MRTR client (ADR-055, ADR-061) end to
// end through the real HTTP handlers and the real RunLauncher/RunManager
// stack, against a Relay-shaped fake MCP server (internal/testutil/mrtrfake).
// This is the harness the plan for #929 calls the basis for #931's
// cross-repository conformance suite: approve, deny, timeout, cancel, and a
// legacy-pinned server, each exercised through the same plumbing an operator
// or an admin token actually reaches.
package run_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/testutil/mrtrfake"
)

// mrtrPolicyYAML grants relay.run_operation WITHOUT Gleipnir's own
// approval:required — Relay owns approval for its own Operations in this
// scenario (relay-646 §4's "Gleipnir's own policy gate ... never on the
// retry"; the demo runbook's decision that in-band Relay approvals are not
// double-gated by default). feedbackTimeout, when non-empty, sets the human
// leg's deadline short enough to exercise the timeout path deterministically.
func mrtrPolicyYAML(feedbackTimeout string) string {
	feedback := ""
	if feedbackTimeout != "" {
		feedback = "\n  feedback:\n    enabled: true\n    timeout: \"" + feedbackTimeout + "\"\n"
	}
	return `
name: mrtr-e2e-policy
trigger:
  type: manual
capabilities:
  tools:
    - tool: relay.run_operation` + feedback + `
agent:
  model: claude-opus-4-5
  task: "restart the gateway"
`
}

// mrtrFixture wires one run's worth of plumbing: a store, a mrtrfake.Server
// registered as the MCP server "relay", a RunLauncher + RunManager, and a chi
// router mounting exactly the routes the scenarios below need (tool-input
// GET/POST, decisions, cancel). fakeOpts configure the fake server (e.g.
// mrtrfake.WithLegacyPin()); feedbackTimeout is threaded into the policy YAML.
func mrtrFixture(t *testing.T, feedbackTimeout string, fakeOpts ...mrtrfake.Option) (*mrtrfake.Server, *run.RunManager, *testutil.RecordingPublisher, *chi.Mux, string, *db.Store) {
	t.Helper()
	ctx := context.Background()

	store := testutil.NewTestStore(t)
	// The identity authed() (tool_input_handler_test.go) stamps into every
	// session -- a real users row so the decision record's actor_user_id
	// (an FK to users) can name it.
	if _, err := store.CreateUser(ctx, db.CreateUserParams{
		ID:           mrtrApproverID,
		Username:     mrtrApprover,
		PasswordHash: "x",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("seed authed() user: %v", err)
	}
	manager := run.NewRunManager()
	// Registered immediately after NewTestStore so cleanup unwinds LIFO: the
	// manager drains in-flight runs BEFORE the store closes underneath them.
	t.Cleanup(manager.Wait)

	fake := mrtrfake.New(fakeOpts...)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	registry := mcp.NewRegistry(store.Queries())
	if _, err := mcp.RegisterServerForTest(ctx, store.Queries(), registry, "relay", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	policyYAML := mrtrPolicyYAML(feedbackTimeout)
	testutil.InsertPolicy(t, store, "p-mrtr", "mrtr-e2e-policy", "manual", policyYAML)
	parsed, err := policy.Parse(policyYAML, "anthropic", "claude-opus-4-5")
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	pub := &testutil.RecordingPublisher{}

	// The scripted LLM: one tool_use round calling relay.run_operation, then
	// an end_turn once the (possibly retried) tool result comes back.
	llmClient := testutil.NewMockLLMClient(
		testutil.MakeToolCallResponse("relay.run_operation", "call-1", map[string]any{
			"selector":  "node:role=gateway",
			"operation": "service.restart",
			"args":      map[string]any{"unit": "api-gateway"},
		}),
		testutil.MakeTextResponse("done"),
	)

	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:    store,
		Resolver: run.NewDefaultToolResolver(registry, nil, nil),
		Manager:  manager,
		AgentFactory: func(cfg agent.Config) (*agent.BoundAgent, error) {
			cfg.LLMClient = llmClient
			return agent.New(cfg)
		},
		Publisher: pub,
	})

	result, err := launcher.Launch(ctx, run.LaunchParams{
		PolicyID:       "p-mrtr",
		TriggerType:    model.TriggerTypeManual,
		TriggerPayload: `{}`,
		ParsedPolicy:   parsed,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	handler := run.NewRunsHandler(store, manager, pub)
	router := chi.NewRouter()
	router.Get("/api/v1/runs/{runID}/tool-input", handler.GetToolInput)
	router.Post("/api/v1/runs/{runID}/tool-input", handler.SubmitToolInput)
	router.Post("/api/v1/runs/{runID}/cancel", handler.Cancel)
	router.Get("/api/v1/runs/{runID}/decisions", handler.ListDecisions)

	return fake, manager, pub, router, result.RunID, store
}

// awaitToolInputCreated blocks until the pause's own "tool_input.created"
// event has been published -- signal-don't-poll: the event fires exactly
// when the tool_input_requests INSERT has committed (agent/state.go), so
// polling the RecordingPublisher's already-captured events (rather than
// wall-clock timing the async side effect itself) is synchronizing on
// something the system already publishes, per the codebase-wide pattern
// (internal/timeout/scanner_test.go's own waitForEvent).
func awaitToolInputCreated(t *testing.T, pub *testutil.RecordingPublisher, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if events := pub.EventsByType("tool_input.created"); len(events) > 0 {
			var payload struct {
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(events[0].Data, &payload); err != nil {
				t.Fatalf("unmarshal tool_input.created payload: %v", err)
			}
			return payload.RequestID
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for tool_input.created")
	return ""
}

// awaitNthToolInputCreated is awaitToolInputCreated for the Nth (1-indexed)
// "tool_input.created" event -- the never-replay-permission scenario pauses
// TWICE for two independent human decisions, and a test needs the SECOND
// pause's own request ID, not the first's.
func awaitNthToolInputCreated(t *testing.T, pub *testutil.RecordingPublisher, n int, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if events := pub.EventsByType("tool_input.created"); len(events) >= n {
			var payload struct {
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(events[n-1].Data, &payload); err != nil {
				t.Fatalf("unmarshal tool_input.created payload: %v", err)
			}
			return payload.RequestID
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tool_input.created #%d", n)
	return ""
}

// finalToolResultIsError returns the is_error field of the run's LAST
// tool_result step -- the fake's own verdict on the call, decoded from the
// audit trail rather than assumed, so a test proves the run actually reached
// a real completed result and not merely "the manager deregistered it".
func finalToolResultIsError(t *testing.T, store *db.Store, runID string) bool {
	t.Helper()
	steps, err := store.Queries().ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: 1000})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var lastContent string
	found := false
	for _, step := range steps {
		if step.Type == string(model.StepTypeToolResult) {
			lastContent = step.Content
			found = true
		}
	}
	if !found {
		t.Fatalf("run %s has no tool_result step", runID)
	}
	var content struct {
		IsError bool `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(lastContent), &content); err != nil {
		t.Fatalf("unmarshal tool_result content %q: %v", lastContent, err)
	}
	return content.IsError
}

// The approver whose username the responder-assertion checks below expect --
// deliberately the same identity authed() (tool_input_handler_test.go)
// stamps into the session, so no second session helper is needed. Its user
// ID ("u1") is what the responder's asserted user_id is checked against.
const (
	mrtrApprover   = "tester"
	mrtrApproverID = "u1"
)

func TestMRTR_Approve(t *testing.T) {
	fake, manager, pub, router, runID, store := mrtrFixture(t, "")

	awaitToolInputCreated(t, pub, 5*time.Second)

	// GET: the message is the fake's summary, byte for byte, attributed to
	// "relay", classified permission.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodGet, "/api/v1/runs/"+runID+"/tool-input", "", model.RoleApprover))
	if w.Code != http.StatusOK {
		t.Fatalf("GET tool-input status = %d, body %s", w.Code, w.Body.String())
	}
	var getBody struct {
		Data run.ToolInputRequestResponse `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&getBody); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if getBody.Data.ServerName != "relay" {
		t.Errorf("server_name = %q, want relay", getBody.Data.ServerName)
	}
	if getBody.Data.ElicitationKind != string(model.ElicitationKindPermission) {
		t.Errorf("elicitation_kind = %q, want permission", getBody.Data.ElicitationKind)
	}
	if len(getBody.Data.Requests) != 1 || getBody.Data.Requests[0].Message == "" {
		t.Fatalf("requests = %+v, want the fake's summary message", getBody.Data.Requests)
	}

	// POST: an approver accepts.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodPost, "/api/v1/runs/"+runID+"/tool-input",
		`{"responses":[{"action":"accept","content":{"confirmed":true}}]}`, model.RoleApprover))
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST tool-input status = %d, body %s", w.Code, w.Body.String())
	}

	if !manager.WaitForDeregistration(runID, 5*time.Second) {
		t.Fatal("run did not finish within deadline")
	}

	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("fake saw %d calls, want 2 (original + retry)", len(calls))
	}
	original, retry := calls[0], calls[1]
	if !retry.IsRetry {
		t.Fatal("second call was not flagged as a retry")
	}

	// The retry echoes the fake's OWN requestState back byte for byte, and
	// the SAME arguments the original call carried -- MRTR's contract is a
	// round trip, not a fresh call the host happens to answer identically.
	if string(retry.RequestState) != string(original.IssuedRequestState) {
		t.Errorf("retry requestState = %s, want the fake's own round-1 blob %s", retry.RequestState, original.IssuedRequestState)
	}
	if !reflect.DeepEqual(retry.Arguments, original.Arguments) {
		t.Errorf("retry arguments = %+v, want identical to the original call's %+v", retry.Arguments, original.Arguments)
	}

	entry, ok := retry.InputResponses["approval"]
	if !ok {
		t.Fatalf("retry inputResponses = %+v, want an \"approval\" entry", retry.InputResponses)
	}
	responder := entry.Responder()
	if responder.Username != mrtrApprover || responder.UserID != mrtrApproverID || responder.Gate != string(model.ElicitationKindPermission) {
		t.Errorf("responder = %+v, want username=%s user_id=%s gate=permission", responder, mrtrApprover, mrtrApproverID)
	}

	// The fake's own verdict is what a run "completing" actually means here:
	// an accepted Operation, not merely the manager deregistering the run.
	if finalToolResultIsError(t, store, runID) {
		t.Error("final tool_result is_error = true, want the fake's accepted verdict")
	}

	// GET /decisions: answered.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodGet, "/api/v1/runs/"+runID+"/decisions", "", model.RoleAuditor))
	var decBody struct {
		Data []run.DecisionSummary `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&decBody); err != nil {
		t.Fatalf("decode decisions response: %v", err)
	}
	if len(decBody.Data) != 1 || decBody.Data[0].Outcome != string(decision.OutcomeAnswered) {
		t.Fatalf("decisions = %+v, want one answered record", decBody.Data)
	}
	if decBody.Data[0].ActorUsername != mrtrApprover {
		t.Errorf("actor_username = %q, want %s", decBody.Data[0].ActorUsername, mrtrApprover)
	}
}

func TestMRTR_Deny(t *testing.T) {
	fake, manager, pub, router, runID, store := mrtrFixture(t, "")

	awaitToolInputCreated(t, pub, 5*time.Second)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodPost, "/api/v1/runs/"+runID+"/tool-input",
		`{"responses":[{"action":"decline"}]}`, model.RoleApprover))
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST tool-input status = %d, body %s", w.Code, w.Body.String())
	}

	if !manager.WaitForDeregistration(runID, 5*time.Second) {
		t.Fatal("run did not finish within deadline")
	}

	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("fake saw %d calls, want 2 (original + retry)", len(calls))
	}
	original, retry := calls[0], calls[1]

	if string(retry.RequestState) != string(original.IssuedRequestState) {
		t.Errorf("retry requestState = %s, want the fake's own round-1 blob %s", retry.RequestState, original.IssuedRequestState)
	}
	if !reflect.DeepEqual(retry.Arguments, original.Arguments) {
		t.Errorf("retry arguments = %+v, want identical to the original call's %+v", retry.Arguments, original.Arguments)
	}

	entry, ok := retry.InputResponses["approval"]
	if !ok || entry.Action != "decline" {
		t.Fatalf("retry inputResponses = %+v, want a decline entry", retry.InputResponses)
	}
	responder := entry.Responder()
	if responder.Username != mrtrApprover || responder.UserID != mrtrApproverID || responder.Gate != string(model.ElicitationKindPermission) {
		t.Errorf("responder = %+v, want username=%s user_id=%s gate=permission", responder, mrtrApprover, mrtrApproverID)
	}

	// Relay's denial is a legitimate tool result the model can reason about,
	// not a host-side failure -- isError is false even though the answer was
	// "no".
	if finalToolResultIsError(t, store, runID) {
		t.Error("final tool_result is_error = true, want a denial reported as a normal (non-error) result")
	}

	// The run completes -- Relay's denial is a legitimate tool result, not a
	// host-side failure.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodGet, "/api/v1/runs/"+runID+"/decisions", "", model.RoleAuditor))
	var decBody struct {
		Data []run.DecisionSummary `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&decBody); err != nil {
		t.Fatalf("decode decisions response: %v", err)
	}
	if len(decBody.Data) != 1 || decBody.Data[0].Outcome != string(decision.OutcomeRejected) {
		t.Fatalf("decisions = %+v, want one rejected record", decBody.Data)
	}
}

func TestMRTR_Timeout(t *testing.T) {
	fake, manager, pub, router, runID, store := mrtrFixture(t, "20ms")

	requestID := awaitToolInputCreated(t, pub, 5*time.Second)

	// Nobody answers. The policy's 20ms feedback timeout ends the wait.
	if !manager.WaitForDeregistration(runID, 5*time.Second) {
		t.Fatal("run did not finish within deadline")
	}

	if calls := fake.Calls(); len(calls) != 1 {
		t.Fatalf("fake saw %d calls, want 1 (no retry on timeout)", len(calls))
	}

	row, err := store.Queries().GetToolInputRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "timed_out" {
		t.Errorf("request status = %q, want timed_out", row.Status)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodGet, "/api/v1/runs/"+runID+"/decisions", "", model.RoleAuditor))
	var decBody struct {
		Data []run.DecisionSummary `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&decBody); err != nil {
		t.Fatalf("decode decisions response: %v", err)
	}
	if len(decBody.Data) != 1 || decBody.Data[0].Outcome != string(decision.OutcomeTimeout) {
		t.Fatalf("decisions = %+v, want one timeout record", decBody.Data)
	}
	if decBody.Data[0].ActorUsername != "" {
		t.Errorf("actor_username = %q, want none -- nobody acted on a timeout", decBody.Data[0].ActorUsername)
	}
}

func TestMRTR_Cancel(t *testing.T) {
	fake, manager, pub, router, runID, store := mrtrFixture(t, "")

	requestID := awaitToolInputCreated(t, pub, 5*time.Second)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodPost, "/api/v1/runs/"+runID+"/cancel", "", model.RoleOperator))
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST cancel status = %d, body %s", w.Code, w.Body.String())
	}

	if !manager.WaitForDeregistration(runID, 5*time.Second) {
		t.Fatal("run did not finish within deadline")
	}

	if calls := fake.Calls(); len(calls) != 1 {
		t.Fatalf("fake saw %d calls, want 1 (no retry on cancel)", len(calls))
	}

	row, err := store.Queries().GetToolInputRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if row.Status != "cancelled" {
		t.Errorf("request status = %q, want cancelled", row.Status)
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodGet, "/api/v1/runs/"+runID+"/decisions", "", model.RoleAuditor))
	var decBody struct {
		Data []run.DecisionSummary `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&decBody); err != nil {
		t.Fatalf("decode decisions response: %v", err)
	}
	if len(decBody.Data) != 1 || decBody.Data[0].Outcome != string(decision.OutcomeCancelled) {
		t.Fatalf("decisions = %+v, want one cancelled record", decBody.Data)
	}
	if decBody.Data[0].ActorUsername != "" {
		t.Errorf("actor_username = %q, want none -- a cancel is not a decision anyone made", decBody.Data[0].ActorUsername)
	}
}

func TestMRTR_LegacyPin(t *testing.T) {
	fake, manager, _, _, runID, _ := mrtrFixture(t, "", mrtrfake.WithLegacyPin())

	if !manager.WaitForDeregistration(runID, 5*time.Second) {
		t.Fatal("run did not finish within deadline")
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("fake saw %d calls, want 1 (legacy pin never pauses)", len(calls))
	}
	if calls[0].IsRetry {
		t.Fatal("legacy pin produced a retry; want the model to receive pending_approval directly")
	}
	if len(calls[0].Meta) != 0 {
		t.Errorf("legacy call carried _meta = %s, want none", calls[0].Meta)
	}
	if len(calls[0].RequestState) != 0 {
		t.Errorf("legacy call carried requestState = %s, want none", calls[0].RequestState)
	}
}

// The Relay demo scenario's ask is consent-only (permission), so an operator
// -- who may answer an information ask, but not a permission one -- must be
// refused. This exercises the SAME role gate TestSubmitToolInput_RoleGateFollowsTheRequestKind
// covers directly against the handler, but through the full HTTP/launcher
// stack, proving the gate is not something only the unit-tested handler path
// enforces.
func TestMRTR_RoleGate403ForOperatorOnPermissionAsk(t *testing.T) {
	_, manager, pub, router, runID, _ := mrtrFixture(t, "")

	awaitToolInputCreated(t, pub, 5*time.Second)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodPost, "/api/v1/runs/"+runID+"/tool-input",
		`{"responses":[{"action":"accept","content":{"confirmed":true}}]}`, model.RoleOperator))
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST tool-input (operator, permission ask) status = %d, body %s", w.Code, w.Body.String())
	}

	// Let an approver settle it, so the run finishes and t.Cleanup(manager.Wait)
	// does not block forever on a pause nobody with the right role answered.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodPost, "/api/v1/runs/"+runID+"/tool-input",
		`{"responses":[{"action":"accept","content":{"confirmed":true}}]}`, model.RoleApprover))
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST tool-input (approver) status = %d, body %s", w.Code, w.Body.String())
	}

	if !manager.WaitForDeregistration(runID, 5*time.Second) {
		t.Fatal("run did not finish within deadline")
	}
}

// The full-stack counterpart of TestBoundAgent_ToolCall_PermissionAskNeverReplays
// (internal/execution/agent): even when the fake re-asks the IDENTICAL
// permission question after a valid answer, the host never replays the
// stored answer onto it -- a second, independent human decision is required,
// reached through the real launcher/manager/HTTP stack this time.
func TestMRTR_NeverReplaysAPermissionAskEvenAcrossARoundTrip(t *testing.T) {
	fake, manager, pub, router, runID, store := mrtrFixture(t, "", mrtrfake.WithReAskOnce())

	first := awaitToolInputCreated(t, pub, 5*time.Second)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodPost, "/api/v1/runs/"+runID+"/tool-input",
		`{"responses":[{"action":"accept","content":{"confirmed":true}}]}`, model.RoleApprover))
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST tool-input (first) status = %d, body %s", w.Code, w.Body.String())
	}

	// A SECOND independent pause, not a silently replayed answer.
	second := awaitNthToolInputCreated(t, pub, 2, 5*time.Second)
	if second == first {
		t.Fatal("the second pause reused the first request ID; want a fresh human ask, not a replay")
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodPost, "/api/v1/runs/"+runID+"/tool-input",
		`{"responses":[{"action":"accept","content":{"confirmed":true}}]}`, model.RoleApprover))
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST tool-input (second) status = %d, body %s", w.Code, w.Body.String())
	}

	if !manager.WaitForDeregistration(runID, 5*time.Second) {
		t.Fatal("run did not finish within deadline")
	}

	// original + re-ask retry + completing retry = 3 calls to the fake.
	calls := fake.Calls()
	if len(calls) != 3 {
		t.Fatalf("fake saw %d calls, want 3 (original + re-ask retry + completing retry)", len(calls))
	}
	if finalToolResultIsError(t, store, runID) {
		t.Error("final tool_result is_error = true, want the fake's accepted verdict")
	}

	// Two independent decision records, both answered by a human -- no
	// replayed_after_ttl outcome, because a permission ask is never eligible
	// for one regardless of how identical the re-ask is.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, authed(http.MethodGet, "/api/v1/runs/"+runID+"/decisions", "", model.RoleAuditor))
	var decBody struct {
		Data []run.DecisionSummary `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&decBody); err != nil {
		t.Fatalf("decode decisions response: %v", err)
	}
	answered := 0
	for _, d := range decBody.Data {
		if d.Outcome == string(decision.OutcomeReplayedAfterTTL) {
			t.Errorf("got a replayed_after_ttl record for a permission ask, want none: %+v", d)
		}
		if d.Outcome == string(decision.OutcomeAnswered) {
			answered++
			if d.ActorUsername != mrtrApprover {
				t.Errorf("answered record actor_username = %q, want %s", d.ActorUsername, mrtrApprover)
			}
		}
	}
	if answered != 2 {
		t.Fatalf("answered decision records = %d, want 2 (one per human ask)", answered)
	}
}

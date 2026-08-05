package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// newToolInputRouter mounts only the two tool-input routes. The role gate under
// test is the one INSIDE the handler — the route middleware in the real router
// only excludes auditors — so this router deliberately applies no middleware:
// every rejection a test observes is the handler's own decision.
func newToolInputRouter(h *run.RunsHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/v1/runs/{runID}/tool-input", h.GetToolInput)
	r.Post("/api/v1/runs/{runID}/tool-input", h.SubmitToolInput)
	return r
}

// insertToolInputRequest creates a pending row of the given kind.
func insertToolInputRequest(t *testing.T, store *db.Store, id, runID string, kind model.ElicitationKind, payload string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.CreateToolInputRequest(context.Background(), db.CreateToolInputRequestParams{
		ID:              id,
		RunID:           runID,
		ServerID:        "srv-tool-input",
		ToolName:        "myserver.deploy",
		CallArgs:        `{"env":"prod"}`,
		RequestState:    `{"cursor":"abc"}`,
		RequestPayload:  payload,
		ElicitationKind: string(kind),
		ExpiresAt:       time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
		CreatedAt:       now,
	})
	if err != nil {
		t.Fatalf("insertToolInputRequest %s: %v", id, err)
	}
}

const permissionPayload = `[{"message":"Deploy to prod?","requested_schema":{"type":"object","properties":{}}}]`
const informationPayload = `[{"message":"Which ticket authorizes this?","requested_schema":{"type":"object","properties":{"ticket":{"type":"string"}}}}]`

// toolInputFixture stands up a store, a run paused on a tool-input request of
// the given kind, and a manager holding a resolver that always succeeds.
func toolInputFixture(t *testing.T, runID string, kind model.ElicitationKind, payload string) (*db.Store, *run.RunManager) {
	t.Helper()
	store := testutil.NewTestStore(t)
	manager := run.NewRunManager()
	t.Cleanup(func() { manager.Deregister(runID) })

	testutil.InsertPolicy(t, store, "p-"+runID, "policy-"+runID, "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, runID, "p-"+runID, model.RunStatusWaitingForFeedback)
	testutil.InsertMcpServer(t, store, "srv-tool-input", "myserver", "http://example.invalid")
	insertToolInputRequest(t, store, "tir-"+runID, runID, kind, payload)

	manager.Register(runID, func() {}, make(chan bool, 1))
	manager.RegisterToolInputResolver(runID, &stubResolver{})
	return store, manager
}

// authed returns a request carrying an authenticated user with the given roles.
func authed(method, target, body string, roles ...model.Role) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	roleStrings := make([]string, len(roles))
	for i, r := range roles {
		roleStrings[i] = string(r)
	}
	return req.WithContext(auth.WithUserContext(req.Context(), "u1", "tester", roleStrings))
}

// The gate that matters: which role may answer depends on what is being asked,
// not on which endpoint was called. A consent-only ask is an authorization
// decision (approver); a request for values is operating work (operator).
func TestSubmitToolInput_RoleGateFollowsTheRequestKind(t *testing.T) {
	cases := []struct {
		name     string
		kind     model.ElicitationKind
		payload  string
		role     model.Role
		wantCode int
	}{
		{"approver answers a permission request", model.ElicitationKindPermission, permissionPayload, model.RoleApprover, http.StatusAccepted},
		{"operator cannot answer a permission request", model.ElicitationKindPermission, permissionPayload, model.RoleOperator, http.StatusForbidden},
		{"auditor cannot answer a permission request", model.ElicitationKindPermission, permissionPayload, model.RoleAuditor, http.StatusForbidden},
		{"admin answers a permission request", model.ElicitationKindPermission, permissionPayload, model.RoleAdmin, http.StatusAccepted},

		{"operator answers an information request", model.ElicitationKindInformation, informationPayload, model.RoleOperator, http.StatusAccepted},
		{"approver cannot answer an information request", model.ElicitationKindInformation, informationPayload, model.RoleApprover, http.StatusForbidden},
		{"auditor cannot answer an information request", model.ElicitationKindInformation, informationPayload, model.RoleAuditor, http.StatusForbidden},
		{"admin answers an information request", model.ElicitationKindInformation, informationPayload, model.RoleAdmin, http.StatusAccepted},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runID := "r-gate-" + string(rune('a'+i))
			store, manager := toolInputFixture(t, runID, tc.kind, tc.payload)

			body := `{"responses":[{"action":"accept","content":{"ok":true}}]}`
			req := authed(http.MethodPost, "/api/v1/runs/"+runID+"/tool-input", body, tc.role)
			w := httptest.NewRecorder()
			newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}

// The kind comes from the persisted row, never from the caller: a client must
// not be able to downgrade an approver-only ask by describing it differently.
func TestSubmitToolInput_KindIsReadFromTheRowNotTheBody(t *testing.T) {
	store, manager := toolInputFixture(t, "r-kind-spoof", model.ElicitationKindPermission, permissionPayload)

	body := `{"elicitation_kind":"information","responses":[{"action":"accept","content":{"ok":true}}]}`
	req := authed(http.MethodPost, "/api/v1/runs/r-kind-spoof/tool-input", body, model.RoleOperator)
	w := httptest.NewRecorder()
	newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — the body must not decide which authority answers", w.Code)
	}
}

func TestSubmitToolInput_Errors(t *testing.T) {
	cases := []struct {
		name       string
		runID      string
		body       string
		setup      func(t *testing.T, store *db.Store, manager *run.RunManager)
		wantCode   int
		wantErrMsg string
	}{
		{
			name:     "unknown run is 404",
			runID:    "r-missing",
			body:     `{"responses":[{"action":"decline"}]}`,
			wantCode: http.StatusNotFound,
		},
		{
			name:  "no pending request is 404",
			runID: "r-no-pending",
			setup: func(t *testing.T, store *db.Store, manager *run.RunManager) {
				testutil.InsertPolicy(t, store, "p-no-pending", "policy-p-no-pending", "webhook", testutil.MinimalWebhookPolicy)
				testutil.InsertRun(t, store, "r-no-pending", "p-no-pending", model.RunStatusRunning)
			},
			body:       `{"responses":[{"action":"decline"}]}`,
			wantCode:   http.StatusNotFound,
			wantErrMsg: "no pending tool input request for this run",
		},
		{
			name:     "empty responses is 400",
			runID:    "r-empty",
			body:     `{"responses":[]}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "malformed body is 400",
			runID:    "r-malformed",
			body:     `not json`,
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			manager := run.NewRunManager()
			if tc.setup != nil {
				tc.setup(t, store, manager)
			}

			req := authed(http.MethodPost, "/api/v1/runs/"+tc.runID+"/tool-input", tc.body, model.RoleAdmin)
			w := httptest.NewRecorder()
			newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantCode, w.Body.String())
			}
			if tc.wantErrMsg != "" {
				var body struct {
					Error string `json:"error"`
				}
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if body.Error != tc.wantErrMsg {
					t.Errorf("error = %q, want %q", body.Error, tc.wantErrMsg)
				}
			}
		})
	}
}

// An answer the waiting side rejects (wrong count, unknown action) is the
// caller's mistake, and the run stays paused and answerable — so it is a 400,
// not a 500 and not a 202.
func TestSubmitToolInput_RejectedAnswerIsABadRequest(t *testing.T) {
	store, manager := toolInputFixture(t, "r-bad-answer", model.ElicitationKindPermission, permissionPayload)
	manager.RegisterToolInputResolver("r-bad-answer", &stubResolver{
		ResolveFunc: func(requestID, body string) error {
			return errBadAnswer
		},
	})

	req := authed(http.MethodPost, "/api/v1/runs/r-bad-answer/tool-input",
		`{"responses":[{"action":"maybe"}]}`, model.RoleApprover)
	w := httptest.NewRecorder()
	newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// A wait that ended between the row read and the delivery is a benign late
// callback, not an error the operator caused.
func TestSubmitToolInput_LateCallbackIsGone(t *testing.T) {
	store, manager := toolInputFixture(t, "r-late", model.ElicitationKindPermission, permissionPayload)
	manager.RegisterToolInputResolver("r-late", &stubResolver{
		ResolveFunc: func(requestID, body string) error {
			return agent.ErrUnknownInputRequestID
		},
	})

	req := authed(http.MethodPost, "/api/v1/runs/r-late/tool-input",
		`{"responses":[{"action":"decline"}]}`, model.RoleApprover)
	w := httptest.NewRecorder()
	newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body: %s", w.Code, w.Body.String())
	}
}

// Reading what a run is blocked on carries the same access as the rest of the
// run detail surface — an auditor who cannot see the question cannot audit the
// decision — and the response says out loud that the text is untrusted.
func TestGetToolInput(t *testing.T) {
	store, manager := toolInputFixture(t, "r-get", model.ElicitationKindInformation, informationPayload)

	for _, role := range []model.Role{model.RoleAuditor, model.RoleOperator, model.RoleApprover, model.RoleAdmin} {
		t.Run(string(role), func(t *testing.T) {
			req := authed(http.MethodGet, "/api/v1/runs/r-get/tool-input", "", role)
			w := httptest.NewRecorder()
			newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
			}

			var body struct {
				Data run.ToolInputRequestResponse `json:"data"`
			}
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Data.ElicitationKind != string(model.ElicitationKindInformation) {
				t.Errorf("elicitation_kind = %q, want information", body.Data.ElicitationKind)
			}
			if body.Data.RequiredRole != string(model.RoleOperator) {
				t.Errorf("required_role = %q, want operator", body.Data.RequiredRole)
			}
			if !body.Data.UntrustedContent {
				t.Error("untrusted_content = false; server-controlled text must always be flagged")
			}
			if len(body.Data.Requests) != 1 || body.Data.Requests[0].Message != "Which ticket authorizes this?" {
				t.Errorf("requests = %+v, want the persisted question", body.Data.Requests)
			}
			if len(body.Data.Requests[0].RequestedSchema) == 0 {
				t.Error("requested_schema is empty; the form cannot be rendered without it")
			}
		})
	}
}

func TestGetToolInput_NoPendingRequest(t *testing.T) {
	store := testutil.NewTestStore(t)
	manager := run.NewRunManager()
	testutil.InsertPolicy(t, store, "p-none", "policy-p-none", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r-none", "p-none", model.RunStatusRunning)

	req := authed(http.MethodGet, "/api/v1/runs/r-none/tool-input", "", model.RoleOperator)
	w := httptest.NewRecorder()
	newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// An unauthenticated request must not reach the row-level gate at all. The
// production router's middleware rejects it first; this asserts the handler
// does not fall open if it is ever mounted without one.
func TestSubmitToolInput_UnauthenticatedIsRejected(t *testing.T) {
	store, manager := toolInputFixture(t, "r-anon", model.ElicitationKindPermission, permissionPayload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/r-anon/tool-input",
		strings.NewReader(`{"responses":[{"action":"decline"}]}`))
	w := httptest.NewRecorder()
	newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", w.Code, w.Body.String())
	}
}

// errBadAnswer stands in for the validation error the waiting side returns for
// a malformed answer.
var errBadAnswer = errors.New("tool input response 0: unknown action \"maybe\"")

// A re-prompt after a server changed its question must arrive with the previous
// question and answer attached (spec §6.5). Without it the operator sees what
// looks like a duplicate of a prompt they already handled, which is exactly
// when a reflexive approval is most costly.
func TestGetToolInput_CarriesThePriorAttempt(t *testing.T) {
	store := testutil.NewTestStore(t)
	manager := run.NewRunManager()
	t.Cleanup(func() { manager.Deregister("r-replay") })

	testutil.InsertPolicy(t, store, "p-replay", "policy-p-replay", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r-replay", "p-replay", model.RunStatusWaitingForFeedback)
	testutil.InsertMcpServer(t, store, "srv-tool-input", "myserver", "http://example.invalid")

	replay := `{"prior_questions":[{"message":"Deploy to prod?"}],` +
		`"prior_answers":[{"Action":"accept","Content":{"confirm":true}}],` +
		`"reason":"the tool re-asked a different question after your answer"}`
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.CreateToolInputRequest(context.Background(), db.CreateToolInputRequestParams{
		ID:              "tir-replay",
		RunID:           "r-replay",
		ServerID:        "srv-tool-input",
		ToolName:        "myserver.deploy",
		CallArgs:        `{"env":"prod"}`,
		RequestState:    `{"cursor":"def"}`,
		RequestPayload:  permissionPayload,
		ElicitationKind: string(model.ElicitationKindPermission),
		ExpiresAt:       time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
		ReplayContext:   &replay,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("CreateToolInputRequest: %v", err)
	}

	req := authed(http.MethodGet, "/api/v1/runs/r-replay/tool-input", "", model.RoleApprover)
	w := httptest.NewRecorder()
	newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data run.ToolInputRequestResponse `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.PriorAttempt == nil {
		t.Fatal("prior_attempt is absent; the operator cannot see that the question changed")
	}
	if len(body.Data.PriorAttempt.PriorQuestions) != 1 ||
		body.Data.PriorAttempt.PriorQuestions[0].Message != "Deploy to prod?" {
		t.Errorf("prior questions = %+v, want the original ask", body.Data.PriorAttempt.PriorQuestions)
	}
	if len(body.Data.PriorAttempt.PriorAnswers) != 1 {
		t.Errorf("prior answers = %+v, want the operator's first answer", body.Data.PriorAttempt.PriorAnswers)
	}
	if body.Data.PriorAttempt.Reason == "" {
		t.Error("prior_attempt.reason is empty; the second prompt has no explanation")
	}
}

// A first ask has nothing before it, and must not claim otherwise.
func TestGetToolInput_FirstAskHasNoPriorAttempt(t *testing.T) {
	store, manager := toolInputFixture(t, "r-first", model.ElicitationKindPermission, permissionPayload)

	req := authed(http.MethodGet, "/api/v1/runs/r-first/tool-input", "", model.RoleApprover)
	w := httptest.NewRecorder()
	newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

	var body struct {
		Data run.ToolInputRequestResponse `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.PriorAttempt != nil {
		t.Errorf("prior_attempt = %+v on a first ask, want absent", body.Data.PriorAttempt)
	}
}

// A corrupt replay context must not block an answerable request: it is framing,
// not the question itself.
func TestGetToolInput_UndecodableReplayContextIsDropped(t *testing.T) {
	store := testutil.NewTestStore(t)
	manager := run.NewRunManager()
	t.Cleanup(func() { manager.Deregister("r-corrupt") })

	testutil.InsertPolicy(t, store, "p-corrupt", "policy-p-corrupt", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r-corrupt", "p-corrupt", model.RunStatusWaitingForFeedback)
	testutil.InsertMcpServer(t, store, "srv-tool-input", "myserver", "http://example.invalid")

	corrupt := `{not json`
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.CreateToolInputRequest(context.Background(), db.CreateToolInputRequestParams{
		ID:              "tir-corrupt",
		RunID:           "r-corrupt",
		ServerID:        "srv-tool-input",
		ToolName:        "myserver.deploy",
		CallArgs:        `{"env":"prod"}`,
		RequestState:    `{"cursor":"def"}`,
		RequestPayload:  permissionPayload,
		ElicitationKind: string(model.ElicitationKindPermission),
		ExpiresAt:       time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
		ReplayContext:   &corrupt,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("CreateToolInputRequest: %v", err)
	}

	req := authed(http.MethodGet, "/api/v1/runs/r-corrupt/tool-input", "", model.RoleApprover)
	w := httptest.NewRecorder()
	newToolInputRouter(run.NewRunsHandler(store, manager, nil)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a bad sidecar must not block the live question; body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data run.ToolInputRequestResponse `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.PriorAttempt != nil {
		t.Errorf("prior_attempt = %+v, want absent when it does not decode", body.Data.PriorAttempt)
	}
	if len(body.Data.Requests) != 1 {
		t.Errorf("requests = %+v, want the live question still rendered", body.Data.Requests)
	}
}

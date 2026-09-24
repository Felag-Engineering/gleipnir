package run_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func newDecisionsRouter(h *run.RunsHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/v1/runs/{runID}/steps", h.ListSteps)
	r.Get("/api/v1/runs/{runID}/decisions", h.ListDecisions)
	return r
}

func getDecisions(t *testing.T, router *chi.Mux, runID string) (int, []run.DecisionSummary) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID+"/decisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	var env struct {
		Data []run.DecisionSummary `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return w.Code, env.Data
}

// The decision records come back on their own endpoint, and the trace endpoint
// is untouched by them. That separation is the ADR-046 split showing through
// the API: /steps is what the model was replayed, /decisions is oversight
// evidence it never saw, and a client that merges them is making a
// presentation decision rather than reading one from the wire.
func TestRunsHandler_ListDecisions(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewTestStore(t)

	testutil.InsertPolicy(t, store, "p-dec", "policy-p-dec", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r-dec", "p-dec", model.RunStatusComplete)
	insertTestStep(t, store, "s-dec-1", "r-dec", 0)

	recorder := decision.NewRecorder(store.Queries())
	if err := recorder.Record(ctx, decision.Record{
		RunID:            "r-dec",
		RequestID:        "req-1",
		Kind:             model.ElicitationKindPermission,
		ToolName:         "deploy.release",
		ChannelEntryID:   "gleipnir.in-app",
		ChannelAssurance: "authenticated",
		LinkMethod:       decision.LinkUnverified,
		ActorExternalID:  "someone@example.com",
		Outcome:          decision.OutcomeAnswered,
		DeadlineSource:   "policy_timeout",
		Considered: []decision.Candidate{
			{EntryID: "entry-1", InstanceID: "inst-1", Reason: "assurance_too_weak"},
		},
		DecidedAt: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	h := run.NewRunsHandler(store, run.NewRunManager(), nil)
	router := newDecisionsRouter(h)

	code, decisions := getDecisions(t, router, "r-dec")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(decisions) != 1 {
		t.Fatalf("got %d decisions, want 1", len(decisions))
	}

	got := decisions[0]
	if got.Type != "tool_permission_request" {
		t.Errorf("type = %q, want tool_permission_request", got.Type)
	}
	// The actor ID travels with the verification flag, and the severity is
	// raised: an approval the host could not tie to a user is the shape a
	// forged approval takes.
	if got.ActorExternalID != "someone@example.com" || got.LinkVerified {
		t.Errorf("actor = %q verified = %v, want the unverified claim", got.ActorExternalID, got.LinkVerified)
	}
	if got.Severity != "warning" {
		t.Errorf("severity = %q, want warning", got.Severity)
	}
	if len(got.Considered) != 1 || got.Considered[0].Reason != "assurance_too_weak" {
		t.Errorf("considered = %+v, want the fall-through record", got.Considered)
	}
	if got.DecidedAt == "" {
		t.Error("decided_at is empty")
	}

	// The trace endpoint is unchanged: nothing about the human exchange leaked
	// into what the model reads.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r-dec/steps", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var stepEnv struct {
		Data []run.StepSummary `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&stepEnv); err != nil {
		t.Fatalf("decode steps: %v", err)
	}
	if len(stepEnv.Data) != 1 {
		t.Fatalf("run has %d steps, want the 1 that was written", len(stepEnv.Data))
	}
	for _, step := range stepEnv.Data {
		if step.Type == "tool_permission_request" || step.Type == "tool_input_request" {
			t.Errorf("a decision record type reached the trace: %q", step.Type)
		}
	}
}

// ActorUsername is looked up from ActorUserID at read time rather than stored
// on the record — a decision record is evidence of what a Gleipnir user ID
// did, and a username is a mutable display value, not part of that evidence.
func TestRunsHandler_ListDecisions_ResolvesActorUsername(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewTestStore(t)

	testutil.InsertPolicy(t, store, "p-actor", "policy-p-actor", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r-actor", "p-actor", model.RunStatusComplete)
	if _, err := store.CreateUser(ctx, db.CreateUserParams{
		ID: "u-alice", Username: "alice", PasswordHash: "x",
		CreatedAt: "2026-08-05T00:00:00Z",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	recorder := decision.NewRecorder(store.Queries())
	if err := recorder.Record(ctx, decision.Record{
		RunID:            "r-actor",
		RequestID:        "req-actor",
		Kind:             model.ElicitationKindPermission,
		ToolName:         "deploy.release",
		ChannelEntryID:   "gleipnir.in-app",
		ChannelAssurance: "authenticated",
		LinkMethod:       decision.LinkSession,
		ActorUserID:      "u-alice",
		Outcome:          decision.OutcomeAnswered,
		DecidedAt:        time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	h := run.NewRunsHandler(store, run.NewRunManager(), nil)
	code, decisions := getDecisions(t, newDecisionsRouter(h), "r-actor")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(decisions) != 1 {
		t.Fatalf("got %d decisions, want 1", len(decisions))
	}
	if decisions[0].ActorUsername != "alice" {
		t.Errorf("actor_username = %q, want alice", decisions[0].ActorUsername)
	}
}

// A deleted user must not fail the whole list — the username is left empty
// rather than surfacing a lookup error for every other decision on the run.
func TestRunsHandler_ListDecisions_UnknownActorLeavesUsernameEmpty(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewTestStore(t)

	testutil.InsertPolicy(t, store, "p-gone", "policy-p-gone", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r-gone", "p-gone", model.RunStatusComplete)
	if _, err := store.CreateUser(ctx, db.CreateUserParams{
		ID: "u-deleted", Username: "gone", PasswordHash: "x",
		CreatedAt: "2026-08-05T00:00:00Z",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	recorder := decision.NewRecorder(store.Queries())
	if err := recorder.Record(ctx, decision.Record{
		RunID:            "r-gone",
		RequestID:        "req-gone",
		Kind:             model.ElicitationKindPermission,
		ToolName:         "deploy.release",
		ChannelEntryID:   "gleipnir.in-app",
		ChannelAssurance: "authenticated",
		LinkMethod:       decision.LinkSession,
		ActorUserID:      "u-deleted",
		Outcome:          decision.OutcomeAnswered,
		DecidedAt:        time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// The user is deleted AFTER the record exists: plugin_audit_events.actor_user_id
	// is ON DELETE SET NULL, but the decision itself lives in the JSON payload,
	// which the FK cascade never touches — this is what lets the record still
	// name the actor after the account behind it is gone.
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM users WHERE id = 'u-deleted'`); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	h := run.NewRunsHandler(store, run.NewRunManager(), nil)
	code, decisions := getDecisions(t, newDecisionsRouter(h), "r-gone")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(decisions) != 1 {
		t.Fatalf("got %d decisions, want 1", len(decisions))
	}
	if decisions[0].ActorUsername != "" {
		t.Errorf("actor_username = %q, want empty for an unresolvable actor", decisions[0].ActorUsername)
	}
	if decisions[0].ActorUserID != "u-deleted" {
		t.Errorf("actor_user_id = %q, want the recorded id preserved", decisions[0].ActorUserID)
	}
}

func TestRunsHandler_ListDecisions_EmptyAndUnknown(t *testing.T) {
	store := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, store, "p-empty", "policy-p-empty", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r-empty", "p-empty", model.RunStatusComplete)

	h := run.NewRunsHandler(store, run.NewRunManager(), nil)
	router := newDecisionsRouter(h)

	code, decisions := getDecisions(t, router, "r-empty")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(decisions) != 0 {
		t.Errorf("got %d decisions for a run with none", len(decisions))
	}

	// An empty list for a run that does not exist would answer "no decisions"
	// to a question about nothing.
	if code, _ := getDecisions(t, router, "r-nonexistent"); code != http.StatusNotFound {
		t.Errorf("unknown run status = %d, want 404", code)
	}
}

// Rows on the shared audit table that are not decision records stay out of the
// response rather than arriving as malformed ones.
func TestRunsHandler_ListDecisions_IgnoresOtherAuditRows(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, store, "p-mixed", "policy-p-mixed", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r-mixed", "p-mixed", model.RunStatusComplete)

	runID := "r-mixed"
	if _, err := store.Queries().InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		EventType:   "plugin_instance_activated",
		Severity:    "info",
		PayloadJson: `{"instance":"inst-1"}`,
		CreatedAt:   "2026-08-05T11:00:00Z",
		RunID:       &runID,
	}); err != nil {
		t.Fatalf("InsertPluginAuditEvent: %v", err)
	}

	h := run.NewRunsHandler(store, run.NewRunManager(), nil)
	code, decisions := getDecisions(t, newDecisionsRouter(h), runID)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(decisions) != 0 {
		t.Errorf("got %d decisions, want none — that row is not a decision record", len(decisions))
	}
}

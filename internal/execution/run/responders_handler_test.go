package run_test

import (
	"context"
	"encoding/json"
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

func insertTestUser(t *testing.T, store *db.Store, id, username string) {
	t.Helper()
	_, err := store.CreateUser(context.Background(), db.CreateUserParams{
		ID:           id,
		Username:     username,
		PasswordHash: "x",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("CreateUser %s: %v", username, err)
	}
}

// newAuthedRunsRouter is newRunsRouter behind a stand-in for the auth
// middleware: it attaches userID to the request context the same way the real
// middleware does.
func newAuthedRunsRouter(h *run.RunsHandler, userID, username string) *chi.Mux {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithUserContext(req.Context(), userID, username, []string{"approver", "operator"})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Post("/api/v1/runs/{runID}/approval", h.SubmitApproval)
	r.Post("/api/v1/runs/{runID}/feedback", h.SubmitFeedback)
	r.Get("/api/v1/runs/{runID}/responders", h.ListResponders)
	return r
}

func TestRunsHandler_SubmitApproval_StampsCaller(t *testing.T) {
	cases := []struct {
		name       string
		decision   string
		wantStatus string
	}{
		{"approve", "approved", "approved"},
		{"reject", "denied", "rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			manager := run.NewRunManager()
			t.Cleanup(manager.Wait)

			insertTestUser(t, store, "u-alice", "alice")
			testutil.InsertPolicy(t, store, "p1", "policy-p1", "webhook", testutil.MinimalWebhookPolicy)
			testutil.InsertRun(t, store, "r1", "p1", model.RunStatusWaitingForApproval)
			testutil.InsertApprovalRequest(t, store, "ar1", "r1", "some_tool")

			manager.Register("r1", func() {}, make(chan bool, 1))
			t.Cleanup(func() { manager.Deregister("r1") })

			router := newAuthedRunsRouter(run.NewRunsHandler(store, manager, nil), "u-alice", "alice")
			// A body-supplied identity must be ignored.
			body := `{"decision":"` + tc.decision + `","decided_by":"u-mallory"}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/r1/approval", strings.NewReader(body))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
			}

			row, err := store.GetApprovalRequest(context.Background(), "ar1")
			if err != nil {
				t.Fatalf("GetApprovalRequest: %v", err)
			}
			if row.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", row.Status, tc.wantStatus)
			}
			if row.DecidedBy == nil || *row.DecidedBy != "u-alice" {
				t.Errorf("decided_by = %v, want u-alice", row.DecidedBy)
			}
		})
	}
}

func TestRunsHandler_SubmitFeedback_StampsCaller(t *testing.T) {
	store := testutil.NewTestStore(t)
	manager := run.NewRunManager()

	insertTestUser(t, store, "u-bob", "bob")
	testutil.InsertPolicy(t, store, "p1", "policy-p1", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r1", "p1", model.RunStatusRunning)

	sm := agent.NewRunStateMachine("r1", model.RunStatusRunning, store.DB(), store.Queries())
	aw := agent.NewAuditWriter(store.Queries())
	t.Cleanup(func() { aw.Close() }) //nolint:errcheck
	fh := agent.NewFeedbackHandler(aw, sm, time.Minute)
	manager.RegisterWithFeedbackResolver("r1", func() {}, make(chan bool, 1), fh)
	t.Cleanup(func() { manager.Deregister("r1") })

	waitDone := make(chan error, 1)
	go func() {
		_, err := fh.Wait(context.Background(), "r1", agent.AskOperatorToolName, "{}", "what now?", time.Minute)
		waitDone <- err
	}()

	var feedbackID string
	deadline := time.Now().Add(5 * time.Second)
	for feedbackID == "" && time.Now().Before(deadline) {
		rows, err := store.GetPendingFeedbackRequestsByRun(context.Background(), "r1")
		if err != nil {
			t.Fatalf("GetPendingFeedbackRequestsByRun: %v", err)
		}
		if len(rows) > 0 {
			feedbackID = rows[0].ID
			break
		}
		time.Sleep(time.Millisecond)
	}
	if feedbackID == "" {
		t.Fatal("timed out waiting for the pending feedback row")
	}

	router := newAuthedRunsRouter(run.NewRunsHandler(store, manager, nil), "u-bob", "bob")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/r1/feedback",
		strings.NewReader(`{"response":"go ahead","responded_by":"u-mallory"}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not unblock")
	}

	row, err := store.GetFeedbackRequest(context.Background(), feedbackID)
	if err != nil {
		t.Fatalf("GetFeedbackRequest: %v", err)
	}
	if row.RespondedBy == nil || *row.RespondedBy != "u-bob" {
		t.Errorf("responded_by = %v, want u-bob", row.RespondedBy)
	}
}

func TestRunsHandler_ListResponders(t *testing.T) {
	store := testutil.NewTestStore(t)
	manager := run.NewRunManager()
	t.Cleanup(manager.Wait)
	ctx := context.Background()

	insertTestUser(t, store, "u-alice", "alice")
	insertTestUser(t, store, "u-gone", "gone")
	testutil.InsertPolicy(t, store, "p1", "policy-p1", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r1", "p1", model.RunStatusRunning)
	for _, id := range []string{"ar-human", "ar-timeout", "ar-deleted"} {
		testutil.InsertApprovalRequest(t, store, id, "r1", "some_tool")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	settle := func(id, status string, by *string) {
		t.Helper()
		if rows, err := store.UpdateApprovalRequestStatus(ctx, db.UpdateApprovalRequestStatusParams{
			Status: status, DecidedAt: &now, DecidedBy: by, ID: id,
		}); err != nil || rows != 1 {
			t.Fatalf("settle %s: rows=%d err=%v", id, rows, err)
		}
	}
	alice, gone := "u-alice", "u-gone"
	settle("ar-human", "approved", &alice)
	settle("ar-timeout", "timeout", nil)
	settle("ar-deleted", "rejected", &gone)

	// Hard-deleting the account must null the reference, not drop the record.
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM users WHERE id = 'u-gone'`); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	router := newAuthedRunsRouter(run.NewRunsHandler(store, manager, nil), "u-alice", "alice")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r1/responders", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data []run.ResponderSummary `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]run.ResponderSummary{}
	for _, s := range envelope.Data {
		byID[s.RequestID] = s
	}
	if len(byID) != 3 {
		t.Fatalf("got %d responders, want 3: %+v", len(byID), envelope.Data)
	}
	if got := byID["ar-human"]; got.Kind != "approval" || got.DecidedBy == nil || got.DecidedBy.Username != "alice" || got.DecidedBy.ID != "u-alice" {
		t.Errorf("ar-human = %+v, want approval decided by alice", got)
	}
	if got := byID["ar-timeout"]; got.Status != "timeout" || got.DecidedBy != nil {
		t.Errorf("ar-timeout = %+v, want timeout with nil decided_by", got)
	}
	if got := byID["ar-deleted"]; got.Status != "rejected" || got.DecidedBy != nil {
		t.Errorf("ar-deleted = %+v, want rejected with nil decided_by", got)
	}
}

func TestRunsHandler_ListResponders_UnknownRun(t *testing.T) {
	store := testutil.NewTestStore(t)
	manager := run.NewRunManager()
	t.Cleanup(manager.Wait)

	router := newAuthedRunsRouter(run.NewRunsHandler(store, manager, nil), "u", "u")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/nope/responders", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

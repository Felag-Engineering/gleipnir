package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// insertFeedbackRequest inserts a pending feedback request for a run.
func insertFeedbackRequest(t *testing.T, s *db.Store, id, runID, toolName, message string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.CreateFeedbackRequest(context.Background(), db.CreateFeedbackRequestParams{
		ID:            id,
		RunID:         runID,
		ToolName:      toolName,
		ProposedInput: "{}",
		Message:       message,
		ExpiresAt:     nil,
		CreatedAt:     now,
	})
	if err != nil {
		t.Fatalf("insertFeedbackRequest %s: %v", id, err)
	}
}

func TestAttentionHandlerEmptyDB(t *testing.T) {
	store := newPolicyHandlerStore(t)
	h := api.NewAttentionHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/attention", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Data api.AttentionResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(env.Data.Items))
	}
}

func TestAttentionHandlerPendingApproval(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "my-policy", "")
	insertTestRun(t, store, "r1", "p1", "waiting_for_approval")
	testutil.InsertApprovalRequest(t, store, "ar1", "r1", "deploy_tool")

	h := api.NewAttentionHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/attention", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Data api.AttentionResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var approval *api.AttentionItem
	for i := range env.Data.Items {
		if env.Data.Items[i].Type == "approval" {
			approval = &env.Data.Items[i]
			break
		}
	}
	if approval == nil {
		t.Fatalf("expected approval item, got: %+v", env.Data.Items)
	}
	if approval.ToolName != "deploy_tool" {
		t.Errorf("tool_name = %q, want %q", approval.ToolName, "deploy_tool")
	}
	if approval.PolicyName != "my-policy" {
		t.Errorf("policy_name = %q, want %q", approval.PolicyName, "my-policy")
	}
	if approval.RunID != "r1" {
		t.Errorf("run_id = %q, want %q", approval.RunID, "r1")
	}
	if approval.ExpiresAt == nil {
		t.Error("expected expires_at to be set")
	}
}

func TestAttentionHandlerPendingFeedback(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "feedback-policy", "")
	insertTestRun(t, store, "r1", "p1", "waiting_for_feedback")
	insertFeedbackRequest(t, store, "fr1", "r1", "ask_operator", "Please review this deployment plan.")

	h := api.NewAttentionHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/attention", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Data api.AttentionResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var feedback *api.AttentionItem
	for i := range env.Data.Items {
		if env.Data.Items[i].Type == "feedback" {
			feedback = &env.Data.Items[i]
			break
		}
	}
	if feedback == nil {
		t.Fatalf("expected feedback item, got: %+v", env.Data.Items)
	}
	if feedback.Message != "Please review this deployment plan." {
		t.Errorf("message = %q, want %q", feedback.Message, "Please review this deployment plan.")
	}
	if feedback.ToolName != "ask_operator" {
		t.Errorf("tool_name = %q, want %q", feedback.ToolName, "ask_operator")
	}
}

func TestAttentionHandlerRecentFailures(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "failing-policy", "")

	// Insert a run that failed recently (within 24h).
	recent := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	testutil.InsertRunWithTime(t, store, "r-failed", "p1", "failed", recent, 0)

	// Insert a run that failed more than 24h ago — should not appear.
	old := time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339Nano)
	testutil.InsertRunWithTime(t, store, "r-old-failed", "p1", "failed", old, 0)

	h := api.NewAttentionHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/attention", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var env struct {
		Data api.AttentionResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var failures []api.AttentionItem
	for _, item := range env.Data.Items {
		if item.Type == "failure" {
			failures = append(failures, item)
		}
	}
	if len(failures) != 1 {
		t.Errorf("expected 1 failure item, got %d: %+v", len(failures), failures)
	}
	if len(failures) > 0 && failures[0].RunID != "r-failed" {
		t.Errorf("failure run_id = %q, want %q", failures[0].RunID, "r-failed")
	}
	if len(failures) > 0 && failures[0].PolicyName != "failing-policy" {
		t.Errorf("failure policy_name = %q, want %q", failures[0].PolicyName, "failing-policy")
	}
}

func TestAttentionHandlerStaleApprovalExcluded(t *testing.T) {
	// An approval_request still in 'pending' status whose run has already
	// moved past waiting_for_approval (e.g. failed due to container restart)
	// must not appear in the attention queue.
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "stale-policy", "")
	insertTestRun(t, store, "r1", "p1", "failed")
	testutil.InsertApprovalRequest(t, store, "ar1", "r1", "some_tool")

	h := api.NewAttentionHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/attention", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var env struct {
		Data api.AttentionResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, item := range env.Data.Items {
		if item.Type == "approval" {
			t.Errorf("stale approval should not appear in attention queue: %+v", item)
		}
	}
}

func TestAttentionHandlerResolvedItemsExcluded(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "pol1", "")
	insertTestRun(t, store, "r1", "p1", "complete")

	// Insert an approval request in 'approved' state — should not appear.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	expiresAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	store.DB().ExecContext(context.Background(),
		`INSERT INTO approval_requests(id, run_id, tool_name, proposed_input, reasoning_summary, status, expires_at, created_at)
		 VALUES('ar1', 'r1', 'tool', '{}', '', 'approved', ?, ?)`,
		expiresAt, now,
	)

	h := api.NewAttentionHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/attention", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var env struct {
		Data api.AttentionResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Items) != 0 {
		t.Errorf("expected 0 items (approved request should be excluded), got %d: %+v",
			len(env.Data.Items), env.Data.Items)
	}
}

// insertToolInputRequest inserts a pending tool-initiated request for a run.
func insertToolInputRequest(t *testing.T, s *db.Store, id, runID, toolName, kind, payload string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	expires := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339Nano)
	source := "policy"
	_, err := s.CreateToolInputRequest(context.Background(), db.CreateToolInputRequestParams{
		ID:              id,
		RunID:           runID,
		ServerID:        "srv-1",
		ToolName:        toolName,
		CallArgs:        "{}",
		RequestState:    "{}",
		RequestPayload:  payload,
		ElicitationKind: kind,
		ExpiresAt:       expires,
		DeadlineSource:  &source,
		CreatedAt:       now,
	})
	if err != nil {
		t.Fatalf("insertToolInputRequest %s: %v", id, err)
	}
}

func attentionItems(t *testing.T, store *db.Store) []api.AttentionItem {
	t.Helper()
	h := api.NewAttentionHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/attention", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data api.AttentionResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Data.Items
}

// A run paused on a tool-initiated request is waiting on a person exactly as an
// approval is. Leaving it out of the queue meant a pause nobody saw until it
// timed out (ADR-055 §6.1).
func TestAttentionHandlerPendingToolInput(t *testing.T) {
	cases := []struct {
		name        string
		kind        string
		payload     string
		wantMessage string
	}{
		{
			name:        "permission ask",
			kind:        "permission",
			payload:     `[{"message":"Delete 12 production records?"}]`,
			wantMessage: "Delete 12 production records?",
		},
		{
			name:        "information ask carries the first question",
			kind:        "information",
			payload:     `[{"message":"Which region?","requested_schema":{"type":"object"}},{"message":"Which tier?"}]`,
			wantMessage: "Which region?",
		},
		{
			name: "a payload with no message still produces a row",
			kind: "permission",
			// A queue row is a pointer to the run, so a request whose text the
			// host cannot read must still be visible — otherwise a malformed
			// server response hides a paused run entirely.
			payload:     `[{}]`,
			wantMessage: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newPolicyHandlerStore(t)
			insertTestPolicy(t, store, "p1", "hitl-policy", "")
			insertTestRun(t, store, "r1", "p1", "waiting_for_feedback")
			testutil.InsertMcpServer(t, store, "srv-1", "srv", "http://srv.invalid")
			insertToolInputRequest(t, store, "tir1", "r1", "deploy.release", tc.kind, tc.payload)

			var item *api.AttentionItem
			for _, got := range attentionItems(t, store) {
				if got.Type == "tool_input" {
					copied := got
					item = &copied
					break
				}
			}
			if item == nil {
				t.Fatal("no tool_input item in the attention queue")
			}
			if item.Message != tc.wantMessage {
				t.Errorf("message = %q, want %q", item.Message, tc.wantMessage)
			}
			if item.ElicitationKind != tc.kind {
				t.Errorf("elicitation_kind = %q, want %q", item.ElicitationKind, tc.kind)
			}
			// The classification decides which role may answer, so the queue
			// carries it rather than making the client fetch the request to
			// find out whether it can render an approve button.
			if !item.UntrustedMessage {
				t.Error("untrusted_message = false; elicitation text is server-controlled (§6.1)")
			}
			if item.ToolName != "deploy.release" {
				t.Errorf("tool_name = %q", item.ToolName)
			}
			if item.RunID != "r1" || item.PolicyName != "hitl-policy" {
				t.Errorf("item = %+v, want the run and policy it belongs to", item)
			}
			if item.ExpiresAt == nil {
				t.Error("expires_at is nil; every pause carries a deadline")
			}
		})
	}
}

// A resolved request leaves the queue. The row survives as the record; the
// queue is what is still waiting on somebody.
func TestAttentionHandlerResolvedToolInputExcluded(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "hitl-policy", "")
	insertTestRun(t, store, "r1", "p1", "waiting_for_feedback")
	testutil.InsertMcpServer(t, store, "srv-1", "srv", "http://srv.invalid")
	insertToolInputRequest(t, store, "tir1", "r1", "deploy.release", "permission", `[{"message":"Proceed?"}]`)

	response := `[{"action":"accept","content":{"ok":true}}]`
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.ResolveToolInputRequest(context.Background(), db.ResolveToolInputRequestParams{
		Response:   &response,
		ResolvedAt: &now,
		ID:         "tir1",
	}); err != nil {
		t.Fatalf("ResolveToolInputRequest: %v", err)
	}

	for _, item := range attentionItems(t, store) {
		if item.Type == "tool_input" {
			t.Errorf("resolved request still in the queue: %+v", item)
		}
	}
}

// The three request types coexist in one queue, ordered by deadline. Ordering
// is the whole reason they share a list: an operator works the most urgent
// thing next regardless of which mechanism produced it.
func TestAttentionHandlerMixedSources(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "mixed-policy", "")
	insertTestRun(t, store, "r-approval", "p1", "waiting_for_approval")
	insertTestRun(t, store, "r-feedback", "p1", "waiting_for_feedback")
	insertTestRun(t, store, "r-toolinput", "p1", "waiting_for_feedback")
	testutil.InsertMcpServer(t, store, "srv-1", "srv", "http://srv.invalid")

	testutil.InsertApprovalRequest(t, store, "ar1", "r-approval", "deploy")
	insertFeedbackRequest(t, store, "fr1", "r-feedback", "ask_operator", "Review?")
	insertToolInputRequest(t, store, "tir1", "r-toolinput", "deploy.release", "permission", `[{"message":"Proceed?"}]`)

	seen := map[string]int{}
	for _, item := range attentionItems(t, store) {
		seen[item.Type]++
	}
	for _, want := range []string{"approval", "feedback", "tool_input"} {
		if seen[want] != 1 {
			t.Errorf("got %d %s items, want 1 (all: %v)", seen[want], want, seen)
		}
	}
}

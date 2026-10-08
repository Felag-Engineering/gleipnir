package agent

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func insertAgentTestUser(t *testing.T, s *db.Store, id, username string) {
	t.Helper()
	if _, err := s.CreateUser(context.Background(), db.CreateUserParams{
		ID: id, Username: username, PasswordHash: "x",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
}

// A plugin-routed approval stamps the user the dispatcher resolved, and leaves
// decided_by NULL when the dispatcher resolved nobody (#684).
func TestApprovalHandler_Wait_PluginStampsDecider(t *testing.T) {
	alice := "u-alice"
	cases := []struct {
		name string
		by   *string
		want sql.NullString
	}{
		{"resolved user is stamped", &alice, sql.NullString{String: alice, Valid: true}},
		{"unresolved actor leaves NULL", nil, sql.NullString{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewTestStore(t)
			insertAgentTestUser(t, s, alice, "alice")
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

			sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
			w := NewAuditWriter(s.Queries())
			defer w.Close() //nolint:errcheck

			mock := &mockApprovalDispatcher{approved: true, deciderUserID: tc.by}
			h := NewApprovalHandler(w, sm, make(chan bool), WithApprovalChannelDispatch(mock, "aud", "p1"))
			if err := h.Wait(context.Background(), "run1", approvalEntry(0), "srv.tool", map[string]any{}); err != nil {
				t.Fatalf("Wait: %v", err)
			}

			var got sql.NullString
			if err := s.DB().QueryRow(`SELECT decided_by FROM approval_requests WHERE run_id = 'run1'`).Scan(&got); err != nil {
				t.Fatalf("query decided_by: %v", err)
			}
			if got != tc.want {
				t.Errorf("decided_by = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFeedbackHandler_Wait_PluginStampsResponder(t *testing.T) {
	bob := "u-bob"
	s := testutil.NewTestStore(t)
	insertAgentTestUser(t, s, bob, "bob")
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	mock := &mockFeedbackDispatcher{response: `{"text":"reply"}`, responderUserID: &bob}
	h := NewFeedbackHandler(w, sm, time.Minute, WithFeedbackChannelDispatch(mock, "aud", "p1"))
	if _, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "question", time.Minute); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	var got sql.NullString
	if err := s.DB().QueryRow(`SELECT responded_by FROM feedback_requests WHERE run_id = 'run1'`).Scan(&got); err != nil {
		t.Fatalf("query responded_by: %v", err)
	}
	if !got.Valid || got.String != bob {
		t.Errorf("responded_by = %+v, want %q", got, bob)
	}
}

// The agent-side timeout claim is a system decision: no user is recorded.
func TestApprovalHandler_Wait_TimeoutLeavesDeciderNull(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewApprovalHandler(w, sm, make(chan bool))
	if err := h.Wait(context.Background(), "run1", approvalEntry(20*time.Millisecond), "srv.tool", map[string]any{}); err == nil {
		t.Fatal("Wait: want a timeout error, got nil")
	}

	var status string
	var decidedBy sql.NullString
	if err := s.DB().QueryRow(`SELECT status, decided_by FROM approval_requests WHERE run_id = 'run1'`).Scan(&status, &decidedBy); err != nil {
		t.Fatalf("query approval row: %v", err)
	}
	if status != "timeout" || decidedBy.Valid {
		t.Errorf("status=%q decided_by=%+v, want timeout with NULL", status, decidedBy)
	}
}

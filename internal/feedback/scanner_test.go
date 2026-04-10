package feedback_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/feedback"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// TestScanner_FeedbackWiring is the feedback-specific integration test that
// verifies the Config callbacks are wired correctly end-to-end:
//   - ListExpiredFeedbackRequests is called with the feedback table
//   - UpdateFeedbackRequestStatus sets status="timed_out"
//   - ErrorCode is model.ErrorCodeFeedbackTimeout in the error step
//   - SSEEventName is "feedback.timed_out" with a "feedback_id" payload key
//   - The run transitions to failed
func TestScanner_FeedbackWiring(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForFeedback)

	// Insert an expired pending feedback directly via SQL so we control the
	// expires_at timestamp. The testutil package has no InsertFeedbackRequest helper.
	_, err := s.DB().Exec(
		`INSERT INTO feedback_requests(id, run_id, tool_name, proposed_input, message, status, expires_at, created_at)
		 VALUES ('f1', 'r1', 'ask_operator', '{}', 'please respond', 'pending', ?, '2024-01-01T00:00:00Z')`,
		time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insert expired feedback: %v", err)
	}

	pub := &testutil.RecordingPublisher{}
	scanner := feedback.NewScanner(s, time.Minute, feedback.WithPublisher(pub))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Feedback status must be "timed_out" (feedback-specific terminal status).
	var feedbackStatus string
	if err := s.DB().QueryRow(`SELECT status FROM feedback_requests WHERE id = 'f1'`).Scan(&feedbackStatus); err != nil {
		t.Fatalf("query feedback: %v", err)
	}
	if feedbackStatus != "timed_out" {
		t.Errorf("feedback status = %q, want timed_out", feedbackStatus)
	}

	// Run must be failed.
	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "failed" {
		t.Errorf("run status = %q, want failed", runStatus)
	}

	// Error step must have code model.ErrorCodeFeedbackTimeout.
	var content string
	if err := s.DB().QueryRow(`SELECT content FROM run_steps WHERE run_id = 'r1' AND type = 'error'`).Scan(&content); err != nil {
		t.Fatalf("query error step: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Errorf("step content not valid JSON: %v", err)
	} else if payload["code"] != string(model.ErrorCodeFeedbackTimeout) {
		t.Errorf("step code = %q, want %q", payload["code"], model.ErrorCodeFeedbackTimeout)
	}

	// SSE event "feedback.timed_out" must have been published with correct payload.
	if events := pub.EventsByType("feedback.timed_out"); len(events) != 1 {
		t.Errorf("feedback.timed_out events = %d, want 1", len(events))
	} else {
		var data map[string]string
		if err := json.Unmarshal(events[0].Data, &data); err != nil {
			t.Errorf("feedback.timed_out payload not valid JSON: %v", err)
		} else {
			if data["feedback_id"] != "f1" {
				t.Errorf("payload feedback_id = %q, want f1", data["feedback_id"])
			}
			if data["status"] != "timed_out" {
				t.Errorf("payload status = %q, want timed_out", data["status"])
			}
		}
	}
}

// TestScanner_NullExpiresAt verifies that a feedback request with NULL expires_at
// (no timeout configured) is never resolved by the scanner. This is feedback-
// specific behavior: ListExpiredFeedbackRequests filters out NULL expires_at rows,
// while approval requests always have a non-NULL expires_at.
func TestScanner_NullExpiresAt(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForFeedback)

	// Insert a pending feedback request with NULL expires_at.
	_, err := s.DB().Exec(
		`INSERT INTO feedback_requests(id, run_id, tool_name, proposed_input, message, status, expires_at, created_at)
		 VALUES ('f1', 'r1', 'ask_operator', '{}', 'please respond', 'pending', NULL, '2024-01-01T00:00:00Z')`,
	)
	if err != nil {
		t.Fatalf("insert feedback with null expires_at: %v", err)
	}

	scanner := feedback.NewScanner(s, time.Minute)
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Feedback must remain pending — NULL expires_at rows must never time out.
	var status string
	if err := s.DB().QueryRow(`SELECT status FROM feedback_requests WHERE id = 'f1'`).Scan(&status); err != nil {
		t.Fatalf("query feedback: %v", err)
	}
	if status != "pending" {
		t.Errorf("feedback status = %q, want pending (NULL expires_at must never time out)", status)
	}
}

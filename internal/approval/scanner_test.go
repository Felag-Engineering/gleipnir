package approval_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/approval"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// TestScanner_ApprovalWiring is the approval-specific integration test that
// verifies the Config callbacks are wired correctly end-to-end:
//   - ListExpiredApprovalRequests is called with the approval table
//   - UpdateApprovalRequestStatus sets status="timeout"
//   - ErrorCode is "approval_timeout" in the error step
//   - SSEEventName is "approval.resolved"
//   - The run transitions to failed
func TestScanner_ApprovalWiring(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForApproval)

	// Insert an expired pending approval directly via SQL so we control the
	// expires_at timestamp (testutil.InsertApprovalRequest always uses future expiry).
	_, err := s.DB().Exec(
		`INSERT INTO approval_requests(id, run_id, tool_name, proposed_input, reasoning_summary, status, expires_at, created_at)
		 VALUES ('a1', 'r1', 'deploy_tool', '{}', 'summary', 'pending', ?, '2024-01-01T00:00:00Z')`,
		time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insert expired approval: %v", err)
	}

	pub := &testutil.RecordingPublisher{}
	scanner := approval.NewScanner(s, time.Minute, approval.WithPublisher(pub))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Approval status must be "timeout" (approval-specific terminal status).
	var approvalStatus string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = 'a1'`).Scan(&approvalStatus); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if approvalStatus != "timeout" {
		t.Errorf("approval status = %q, want timeout", approvalStatus)
	}

	// Run must be failed.
	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "failed" {
		t.Errorf("run status = %q, want failed", runStatus)
	}

	// Error step must have code "approval_timeout".
	var content string
	if err := s.DB().QueryRow(`SELECT content FROM run_steps WHERE run_id = 'r1' AND type = 'error'`).Scan(&content); err != nil {
		t.Fatalf("query error step: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Errorf("step content not valid JSON: %v", err)
	} else if payload["code"] != "approval_timeout" {
		t.Errorf("step code = %q, want approval_timeout", payload["code"])
	}

	// SSE event "approval.resolved" must have been published with correct payload.
	if events := pub.EventsByType("approval.resolved"); len(events) != 1 {
		t.Errorf("approval.resolved events = %d, want 1", len(events))
	} else {
		var data map[string]string
		if err := json.Unmarshal(events[0].Data, &data); err != nil {
			t.Errorf("approval.resolved payload not valid JSON: %v", err)
		} else {
			if data["approval_id"] != "a1" {
				t.Errorf("payload approval_id = %q, want a1", data["approval_id"])
			}
			if data["status"] != "timeout" {
				t.Errorf("payload status = %q, want timeout", data["status"])
			}
		}
	}
}

// TestScannerConstants verifies that the model package constants expected by
// the approval scanner have not been changed to unexpected values.
func TestScannerConstants(t *testing.T) {
	if model.RunStatusFailed != "failed" {
		t.Errorf("RunStatusFailed = %q, want failed", model.RunStatusFailed)
	}
	if model.ApprovalStatusTimeout != "timeout" {
		t.Errorf("ApprovalStatusTimeout = %q, want timeout", model.ApprovalStatusTimeout)
	}
}

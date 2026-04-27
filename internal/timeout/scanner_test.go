// Package timeout_test exercises the generic scan loop using the approval
// domain as its concrete backend. Approval DB queries are used here because
// they are the simpler of the two (no pointer-typed cutoff, no NULL expires_at
// semantics). All generic scanner behaviors — race guard, crash-recovery guard,
// step numbering, concurrent scans, event publishing, start/stop — are tested
// here so they do not need to be duplicated in the approval or feedback tests.
package timeout_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/timeout"
)

// approvalConfig returns a timeout.Config wired to the approval_requests table.
// This is the concrete backend for the generic scanner tests.
func approvalConfig(store *db.Store) timeout.Config {
	return timeout.Config{
		Name: "approval",
		ListExpired: func(ctx context.Context, cutoff string) ([]timeout.ExpiredItem, error) {
			rows, err := store.Queries().ListExpiredApprovalRequests(ctx, cutoff)
			if err != nil {
				return nil, err
			}
			items := make([]timeout.ExpiredItem, len(rows))
			for i, r := range rows {
				items[i] = timeout.ExpiredItem{ID: r.ID, RunID: r.RunID, ToolName: r.ToolName}
			}
			return items, nil
		},
		ClaimTimeout: func(ctx context.Context, id string, now string) (int64, error) {
			return store.Queries().UpdateApprovalRequestStatus(ctx, db.UpdateApprovalRequestStatusParams{
				Status:    string(model.ApprovalStatusTimeout),
				DecidedAt: &now,
				Note:      nil,
				ID:        id,
			})
		},
		WaitingRunStatus: model.RunStatusWaitingForApproval,
		ErrorCode:        "approval_timeout",
		ErrorMessage: func(toolName string) string {
			return fmt.Sprintf("approval timeout: %s not approved within timeout window", toolName)
		},
		SSEEventName: "approval.resolved",
		SSEPayload: func(id, runID string) map[string]string {
			return map[string]string{
				"approval_id": id,
				"run_id":      runID,
				"status":      string(model.ApprovalStatusTimeout),
			}
		},
	}
}

// insertApprovalRequest inserts a pending approval request with the given
// expiresAt timestamp (RFC3339Nano string). The testutil helper always sets
// a future expiry, so we need this local helper for past-expiry tests.
func insertApprovalRequest(t *testing.T, s *db.Store, id, runID, toolName, expiresAt string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO approval_requests(id, run_id, tool_name, proposed_input, reasoning_summary, status, expires_at, created_at)
		 VALUES (?, ?, ?, '{}', 'summary', 'pending', ?, '2024-01-01T00:00:00Z')`,
		id, runID, toolName, expiresAt,
	)
	if err != nil {
		t.Fatalf("insertApprovalRequest %s: %v", id, err)
	}
}

func pastTimestamp() string {
	return time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
}

func futureTimestamp() string {
	return time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339Nano)
}

// countErrorSteps returns the number of error-type run_steps for a run.
func countErrorSteps(t *testing.T, s *db.Store, runID string) int {
	t.Helper()
	var count int
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM run_steps WHERE run_id = ? AND type = 'error'`, runID,
	).Scan(&count); err != nil {
		t.Fatalf("countErrorSteps %s: %v", runID, err)
	}
	return count
}

// countEventsByType counts how many recorded events match the given type.
func countEventsByType(pub *testutil.RecordingPublisher, eventType string) int {
	return len(pub.EventsByType(eventType))
}

func TestScanner_NoExpiredRequests(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForApproval)
	insertApprovalRequest(t, s, "a1", "r1", "some_tool", futureTimestamp())

	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var status string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = 'a1'`).Scan(&status); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if status != "pending" {
		t.Errorf("approval status = %q, want pending", status)
	}

	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "waiting_for_approval" {
		t.Errorf("run status = %q, want waiting_for_approval", runStatus)
	}
}

func TestScanner_ExpiredRequest_MarksTimeoutAndFails(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForApproval)
	insertApprovalRequest(t, s, "a1", "r1", "deploy_tool", pastTimestamp())

	before := time.Now()
	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Approval must be marked timeout with a decided_at.
	var approvalStatus, decidedAt string
	if err := s.DB().QueryRow(`SELECT status, COALESCE(decided_at, '') FROM approval_requests WHERE id = 'a1'`).Scan(&approvalStatus, &decidedAt); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if approvalStatus != "timeout" {
		t.Errorf("approval status = %q, want timeout", approvalStatus)
	}
	if decidedAt == "" {
		t.Error("decided_at is empty, want a timestamp")
	} else {
		ts, err := time.Parse(time.RFC3339Nano, decidedAt)
		if err != nil {
			t.Errorf("decided_at %q not valid RFC3339Nano: %v", decidedAt, err)
		} else if ts.Before(before) {
			t.Errorf("decided_at %v is before test start %v", ts, before)
		}
	}

	// Run must be marked failed with completed_at and an error message.
	var runStatus, completedAt, runError string
	if err := s.DB().QueryRow(`SELECT status, COALESCE(completed_at, ''), COALESCE(error, '') FROM runs WHERE id = 'r1'`).Scan(&runStatus, &completedAt, &runError); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "failed" {
		t.Errorf("run status = %q, want failed", runStatus)
	}
	if completedAt == "" {
		t.Error("completed_at is empty, want a timestamp")
	}
	if runError == "" {
		t.Error("run error field is empty, want an error message")
	}

	// An error step must exist with approval_timeout code.
	var stepType, content string
	if err := s.DB().QueryRow(`SELECT type, content FROM run_steps WHERE run_id = 'r1'`).Scan(&stepType, &content); err != nil {
		t.Fatalf("query run_step: %v", err)
	}
	if stepType != "error" {
		t.Errorf("step type = %q, want error", stepType)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Errorf("step content not valid JSON: %v", err)
	} else if payload["code"] != "approval_timeout" {
		t.Errorf("step content code = %q, want approval_timeout", payload["code"])
	}
}

func TestScanner_AlreadyDecided_NotReprocessed(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusComplete)

	// Insert an approval that is already decided (not pending) but has an
	// expires_at in the past. ListExpiredApprovalRequests filters on
	// status='pending', so this row must never be returned.
	decidedAt := pastTimestamp()
	_, err := s.DB().Exec(
		`INSERT INTO approval_requests(id, run_id, tool_name, proposed_input, reasoning_summary, status, decided_at, expires_at, created_at)
		 VALUES ('a1', 'r1', 'tool', '{}', 'summary', 'approved', ?, ?, '2024-01-01T00:00:00Z')`,
		decidedAt, pastTimestamp(),
	)
	if err != nil {
		t.Fatalf("insert approved request: %v", err)
	}

	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var status string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = 'a1'`).Scan(&status); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if status != "approved" {
		t.Errorf("approval status = %q, want approved (must not be re-processed)", status)
	}
}

func TestScanner_MultipleExpiredAcrossRuns(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForApproval)
	testutil.InsertRun(t, s, "r2", "p1", model.RunStatusWaitingForApproval)
	insertApprovalRequest(t, s, "a1", "r1", "tool_a", pastTimestamp())
	insertApprovalRequest(t, s, "a2", "r2", "tool_b", pastTimestamp())

	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	for _, approvalID := range []string{"a1", "a2"} {
		var status string
		if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = ?`, approvalID).Scan(&status); err != nil {
			t.Fatalf("query approval %s: %v", approvalID, err)
		}
		if status != "timeout" {
			t.Errorf("approval %s status = %q, want timeout", approvalID, status)
		}
	}

	for _, runID := range []string{"r1", "r2"} {
		var status string
		if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = ?`, runID).Scan(&status); err != nil {
			t.Fatalf("query run %s: %v", runID, err)
		}
		if status != "failed" {
			t.Errorf("run %s status = %q, want failed", runID, status)
		}
	}
}

func TestScanner_RunAlreadyInterrupted_StillTimedOut(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	// Run was already moved to interrupted (e.g. by ScanOrphanedRuns on restart).
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusInterrupted)
	insertApprovalRequest(t, s, "a1", "r1", "tool_x", pastTimestamp())

	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Approval must still be marked timeout.
	var approvalStatus string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = 'a1'`).Scan(&approvalStatus); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if approvalStatus != "timeout" {
		t.Errorf("approval status = %q, want timeout", approvalStatus)
	}

	// Run must remain interrupted (not changed to failed).
	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "interrupted" {
		t.Errorf("run status = %q, want interrupted (must not be changed)", runStatus)
	}
}

func TestScanner_PublisherReceivesEvents(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForApproval)
	insertApprovalRequest(t, s, "a1", "r1", "tool_y", pastTimestamp())

	pub := &testutil.RecordingPublisher{}
	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s), timeout.WithPublisher(pub))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	wantTypes := map[string]bool{
		"run.status_changed": false,
		"approval.resolved":  false,
	}
	for _, e := range pub.Events {
		wantTypes[e.Type] = true
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Errorf("expected event type %q was not published", typ)
		}
	}
}

func TestScanner_StartStop(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForApproval)
	insertApprovalRequest(t, s, "a1", "r1", "tool_z", pastTimestamp())

	ctx, cancel := context.WithCancel(context.Background())
	// Short interval so the scan fires quickly in the test.
	scanner := timeout.NewScanner(s, 20*time.Millisecond, approvalConfig(s))
	scanner.Start(ctx)

	// Wait long enough for at least one scan tick.
	time.Sleep(100 * time.Millisecond)
	cancel()

	// The approval should have been resolved by now.
	var status string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = 'a1'`).Scan(&status); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if status != "timeout" {
		t.Errorf("approval status = %q after scanner ran, want timeout", status)
	}
}

func TestScanner_StepNumberContinuesExistingSteps(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForApproval)
	insertApprovalRequest(t, s, "a1", "r1", "tool_w", pastTimestamp())

	// Pre-insert 2 steps so the error step should get step_number = 2.
	_, err := s.DB().Exec(
		`INSERT INTO run_steps(id, run_id, step_number, type, content, created_at)
		 VALUES ('s1', 'r1', 0, 'thought', '{}', '2024-01-01T00:00:00Z'),
		        ('s2', 'r1', 1, 'tool_call', '{}', '2024-01-01T00:00:00Z')`,
	)
	if err != nil {
		t.Fatalf("insert existing steps: %v", err)
	}

	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var stepNumber int64
	if err := s.DB().QueryRow(`SELECT step_number FROM run_steps WHERE run_id = 'r1' AND type = 'error'`).Scan(&stepNumber); err != nil {
		t.Fatalf("query error step: %v", err)
	}
	if stepNumber != 2 {
		t.Errorf("error step step_number = %d, want 2", stepNumber)
	}
}

// TestScanner_ConcurrentScans_SingleTimeout verifies that N concurrent Scan
// calls racing on a single expired approval row each produce exactly one
// timeout approval, one failed run, one error step, and one run.status_changed
// event.
//
// This test is the authoritative proof that the rows==0 guard in resolveTimeout
// prevents duplicate side-effects under concurrency.
func TestScanner_ConcurrentScans_SingleTimeout(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForApproval)
	insertApprovalRequest(t, s, "a1", "r1", "dangerous_tool", pastTimestamp())

	pub := &testutil.RecordingPublisher{}
	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s), timeout.WithPublisher(pub))

	const goroutines = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start // wait for all goroutines to be ready before racing
			if err := scanner.Scan(context.Background()); err != nil {
				t.Errorf("Scan: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	// Approval must be timeout.
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

	// Exactly one error step must exist — duplicate writes would indicate a race.
	if n := countErrorSteps(t, s, "r1"); n != 1 {
		t.Errorf("error steps = %d, want exactly 1 (duplicate indicates race)", n)
	}

	// Exactly one run.status_changed event — duplicate would indicate a race.
	if n := countEventsByType(pub, "run.status_changed"); n != 1 {
		t.Errorf("run.status_changed events = %d, want exactly 1", n)
	}
}

// TestScanner_ConcurrentScans_MultipleRuns_SingleTimeoutEach verifies that N
// concurrent Scan calls each produce the correct outcome (exactly one error
// step and one event) for every one of M distinct run+approval pairs.
func TestScanner_ConcurrentScans_MultipleRuns_SingleTimeoutEach(t *testing.T) {
	const numRuns = 5
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	for i := 0; i < numRuns; i++ {
		runID := fmt.Sprintf("r%d", i)
		approvalID := fmt.Sprintf("a%d", i)
		testutil.InsertRun(t, s, runID, "p1", model.RunStatusWaitingForApproval)
		insertApprovalRequest(t, s, approvalID, runID, "tool", pastTimestamp())
	}

	pub := &testutil.RecordingPublisher{}
	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s), timeout.WithPublisher(pub))

	const goroutines = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			if err := scanner.Scan(context.Background()); err != nil {
				t.Errorf("Scan: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	for i := 0; i < numRuns; i++ {
		runID := fmt.Sprintf("r%d", i)
		approvalID := fmt.Sprintf("a%d", i)

		var approvalStatus string
		if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = ?`, approvalID).Scan(&approvalStatus); err != nil {
			t.Fatalf("query approval %s: %v", approvalID, err)
		}
		if approvalStatus != "timeout" {
			t.Errorf("approval %s status = %q, want timeout", approvalID, approvalStatus)
		}

		var runStatus string
		if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = ?`, runID).Scan(&runStatus); err != nil {
			t.Fatalf("query run %s: %v", runID, err)
		}
		if runStatus != "failed" {
			t.Errorf("run %s status = %q, want failed", runID, runStatus)
		}

		if n := countErrorSteps(t, s, runID); n != 1 {
			t.Errorf("run %s: error steps = %d, want exactly 1", runID, n)
		}
	}

	// Each of the 5 runs should produce exactly one run.status_changed event.
	if n := countEventsByType(pub, "run.status_changed"); n != numRuns {
		t.Errorf("run.status_changed events = %d, want %d", n, numRuns)
	}
}

// TestScanner_DoubleTransitionProtection_RunAlreadyFailed verifies that when
// the run is already in a terminal state (e.g. failed), the scanner's
// early-return branch prevents a second error step or event from being written.
func TestScanner_DoubleTransitionProtection_RunAlreadyFailed(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	// Run is already in a terminal state — scanner must not write another error step.
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusFailed)
	insertApprovalRequest(t, s, "a1", "r1", "tool", pastTimestamp())

	pub := &testutil.RecordingPublisher{}
	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s), timeout.WithPublisher(pub))
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// The approval is resolved (status transitions to timeout via the guarded UPDATE).
	var approvalStatus string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = 'a1'`).Scan(&approvalStatus); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if approvalStatus != "timeout" {
		t.Errorf("approval status = %q, want timeout", approvalStatus)
	}

	// Run remains failed (completed_at must not be overwritten).
	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "failed" {
		t.Errorf("run status = %q, want failed (must not change)", runStatus)
	}

	// No new error step should have been added for a run that's already failed.
	if n := countErrorSteps(t, s, "r1"); n != 0 {
		t.Errorf("error steps = %d, want 0 (run was already failed)", n)
	}

	// No events should be published because the run was not in waiting_for_approval.
	if n := countEventsByType(pub, "run.status_changed"); n != 0 {
		t.Errorf("run.status_changed events = %d, want 0", n)
	}
}

// TestScanner_RestartInterrupted_ThenTimeout exercises the path where
// ScanOrphanedRuns marks the run 'interrupted' on startup, and then the
// scanner runs afterwards. The scanner must mark the approval timeout but
// must NOT add a second error step or transition the run again.
func TestScanner_RestartInterrupted_ThenTimeout(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusWaitingForApproval)
	insertApprovalRequest(t, s, "a1", "r1", "tool", pastTimestamp())

	// Simulate what ScanOrphanedRuns does on process restart: mark the run
	// interrupted and insert an error step with code='interrupted'.
	if err := s.ScanOrphanedRuns(context.Background(), slog.Default()); err != nil {
		t.Fatalf("ScanOrphanedRuns: %v", err)
	}

	// Confirm run is now interrupted with one error step.
	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run after ScanOrphanedRuns: %v", err)
	}
	if runStatus != "interrupted" {
		t.Fatalf("run status after ScanOrphanedRuns = %q, want interrupted", runStatus)
	}
	if n := countErrorSteps(t, s, "r1"); n != 1 {
		t.Fatalf("error steps after ScanOrphanedRuns = %d, want 1", n)
	}

	pub := &testutil.RecordingPublisher{}
	scanner := timeout.NewScanner(s, time.Minute, approvalConfig(s), timeout.WithPublisher(pub))
	// Scan called synchronously — avoids wall-clock races under -race.
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan after restart: %v", err)
	}

	// Approval must be timeout — the scanner still resolves its status.
	var approvalStatus string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = 'a1'`).Scan(&approvalStatus); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if approvalStatus != "timeout" {
		t.Errorf("approval status = %q, want timeout", approvalStatus)
	}

	// Run must remain interrupted — scanner skips run update for non-waiting runs.
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run after Scan: %v", err)
	}
	if runStatus != "interrupted" {
		t.Errorf("run status = %q, want interrupted (must not change)", runStatus)
	}

	// Still exactly one error step (only from ScanOrphanedRuns, not from the scanner).
	if n := countErrorSteps(t, s, "r1"); n != 1 {
		t.Errorf("error steps = %d, want 1 (scanner must not add a second)", n)
	}

	// Scanner must not publish events when the run is not in waiting_for_approval.
	if n := countEventsByType(pub, "run.status_changed"); n != 0 {
		t.Errorf("run.status_changed events from scanner = %d, want 0", n)
	}
}

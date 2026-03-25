package approval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// newTestStore opens a fresh SQLite test DB and applies all migrations.
func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func insertPolicy(t *testing.T, s *db.Store, id string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO policies(id, name, trigger_type, yaml, created_at, updated_at)
		 VALUES (?, ?, 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		id, "policy-"+id,
	)
	if err != nil {
		t.Fatalf("insertPolicy %s: %v", id, err)
	}
}

func insertRun(t *testing.T, s *db.Store, id, policyID, status string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
		 VALUES (?, ?, ?, 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		id, policyID, status,
	)
	if err != nil {
		t.Fatalf("insertRun %s: %v", id, err)
	}
}

// insertApprovalRequest inserts a pending approval request with the given
// expiresAt timestamp (RFC3339Nano string).
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

// pastTimestamp returns a timestamp guaranteed to be in the past relative to
// the scan cutoff used during tests.
func pastTimestamp() string {
	return time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
}

// futureTimestamp returns a timestamp that will not be reached during the test.
func futureTimestamp() string {
	return time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339Nano)
}

func TestScanner_NoExpiredRequests(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_approval")
	insertApprovalRequest(t, s, "a1", "r1", "some_tool", futureTimestamp())

	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
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
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_approval")
	insertApprovalRequest(t, s, "a1", "r1", "deploy_tool", pastTimestamp())

	before := time.Now()
	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Approval must be marked timeout with a decided_at.
	var approvalStatus string
	var decidedAt string
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

func TestScanner_AlreadyDecidedApproval_NotReprocessed(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "complete")

	// Insert an approval that is already decided (not pending) but has an
	// expires_at in the past. The query filters on status = 'pending', so this
	// should not be returned.
	decidedAt := pastTimestamp()
	_, err := s.DB().Exec(
		`INSERT INTO approval_requests(id, run_id, tool_name, proposed_input, reasoning_summary, status, decided_at, expires_at, created_at)
		 VALUES ('a1', 'r1', 'tool', '{}', 'summary', 'approved', ?, ?, '2024-01-01T00:00:00Z')`,
		decidedAt, pastTimestamp(),
	)
	if err != nil {
		t.Fatalf("insert approved request: %v", err)
	}

	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// The approval status must remain approved.
	var status string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = 'a1'`).Scan(&status); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if status != "approved" {
		t.Errorf("approval status = %q, want approved (must not be re-processed)", status)
	}
}

func TestScanner_MultipleExpiredAcrossRuns(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_approval")
	insertRun(t, s, "r2", "p1", "waiting_for_approval")
	insertApprovalRequest(t, s, "a1", "r1", "tool_a", pastTimestamp())
	insertApprovalRequest(t, s, "a2", "r2", "tool_b", pastTimestamp())

	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
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

func TestScanner_RunAlreadyInterrupted_ApprovalStillTimedOut(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	// Run was already moved to interrupted (e.g. by ScanOrphanedRuns on restart).
	insertRun(t, s, "r1", "p1", "interrupted")
	insertApprovalRequest(t, s, "a1", "r1", "tool_x", pastTimestamp())

	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
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

// capturePublisher records published events for test assertions.
type capturePublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	eventType string
	data      json.RawMessage
}

func (p *capturePublisher) Publish(eventType string, data json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, publishedEvent{eventType: eventType, data: data})
}

func (p *capturePublisher) eventTypes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	types := make([]string, len(p.events))
	for i, e := range p.events {
		types[i] = e.eventType
	}
	return types
}

func TestScanner_PublisherReceivesEvents(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_approval")
	insertApprovalRequest(t, s, "a1", "r1", "tool_y", pastTimestamp())

	pub := &capturePublisher{}
	scanner := NewScanner(s, time.Minute, WithPublisher(pub))
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	types := pub.eventTypes()
	wantTypes := map[string]bool{
		"run.status_changed": false,
		"approval.resolved":  false,
	}
	for _, typ := range types {
		wantTypes[typ] = true
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Errorf("expected event type %q was not published", typ)
		}
	}
}

func TestScanner_StartStop(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_approval")
	insertApprovalRequest(t, s, "a1", "r1", "tool_z", pastTimestamp())

	ctx, cancel := context.WithCancel(context.Background())
	// Short interval so the scan fires quickly in the test.
	scanner := NewScanner(s, 20*time.Millisecond)
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
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_approval")
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

	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	var stepNumber int64
	if err := s.DB().QueryRow(`SELECT step_number FROM run_steps WHERE run_id = 'r1' AND type = 'error'`).Scan(&stepNumber); err != nil {
		t.Fatalf("query error step: %v", err)
	}
	if stepNumber != 2 {
		t.Errorf("error step step_number = %d, want 2", stepNumber)
	}
}

// Verify that the model package constants used in the scanner match expectations.
func TestScannerConstants(t *testing.T) {
	if model.RunStatusFailed != "failed" {
		t.Errorf("RunStatusFailed = %q, want failed", model.RunStatusFailed)
	}
	if model.ApprovalStatusTimeout != "timeout" {
		t.Errorf("ApprovalStatusTimeout = %q, want timeout", model.ApprovalStatusTimeout)
	}
}

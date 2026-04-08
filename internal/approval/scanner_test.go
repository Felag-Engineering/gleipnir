package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// countEventsByType counts how many published events of a specific type exist in pubs.
func countEventsByType(pubs *capturePublisher, eventType string) int {
	pubs.mu.Lock()
	defer pubs.mu.Unlock()
	n := 0
	for _, e := range pubs.events {
		if e.eventType == eventType {
			n++
		}
	}
	return n
}

// TestScanner_ConcurrentScans_SingleTimeout verifies that N concurrent scanner.scan
// calls racing on a single expired approval row each produce exactly one timeout
// approval, one failed run, one error step, and one run.status_changed event.
//
// This test is the authoritative proof that the rows==0 guard in resolveTimeout
// prevents duplicate side-effects under concurrency.
func TestScanner_ConcurrentScans_SingleTimeout(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_approval")
	insertApprovalRequest(t, s, "a1", "r1", "dangerous_tool", pastTimestamp())

	pub := &capturePublisher{}
	scanner := NewScanner(s, time.Minute, WithPublisher(pub))

	const goroutines = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start // wait for all goroutines to be ready before racing
			if err := scanner.scan(context.Background()); err != nil {
				t.Errorf("scan: %v", err)
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
// concurrent scanner.scan calls each produce the correct outcome (exactly one
// error step and one event) for every one of M distinct run+approval pairs.
func TestScanner_ConcurrentScans_MultipleRuns_SingleTimeoutEach(t *testing.T) {
	const numRuns = 5
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	for i := 0; i < numRuns; i++ {
		runID := fmt.Sprintf("r%d", i)
		approvalID := fmt.Sprintf("a%d", i)
		insertRun(t, s, runID, "p1", "waiting_for_approval")
		insertApprovalRequest(t, s, approvalID, runID, "tool", pastTimestamp())
	}

	pub := &capturePublisher{}
	scanner := NewScanner(s, time.Minute, WithPublisher(pub))

	const goroutines = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			if err := scanner.scan(context.Background()); err != nil {
				t.Errorf("scan: %v", err)
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

// TestScanner_DoubleTransitionProtection_RunAlreadyFailed verifies that when the
// run is already in a non-waiting_for_approval status (e.g. failed), the scanner's
// early-return branch prevents a second error step or event from being written.
func TestScanner_DoubleTransitionProtection_RunAlreadyFailed(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	// Run is already in a terminal state — scanner must not write another error step.
	insertRun(t, s, "r1", "p1", "failed")
	insertApprovalRequest(t, s, "a1", "r1", "tool", pastTimestamp())

	pub := &capturePublisher{}
	scanner := NewScanner(s, time.Minute, WithPublisher(pub))
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
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
// approval scanner runs afterwards. The scanner must mark the approval timeout
// but must NOT add a second error step or transition the run again.
func TestScanner_RestartInterrupted_ThenTimeout(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_approval")
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

	pub := &capturePublisher{}
	scanner := NewScanner(s, time.Minute, WithPublisher(pub))
	// scan() called synchronously — avoids wall-clock races under -race.
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan after restart: %v", err)
	}

	// Approval must be timeout — the scanner still resolves its status.
	var approvalStatus string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = 'a1'`).Scan(&approvalStatus); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if approvalStatus != "timeout" {
		t.Errorf("approval status = %q, want timeout", approvalStatus)
	}

	// Run must remain interrupted — scanner skips the run update for non-waiting runs.
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run after scan: %v", err)
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

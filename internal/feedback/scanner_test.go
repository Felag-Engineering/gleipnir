package feedback

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

// insertFeedbackRequest inserts a pending feedback request with the given
// expiresAt (pass empty string for NULL).
func insertFeedbackRequest(t *testing.T, s *db.Store, id, runID, toolName, expiresAt string) {
	t.Helper()
	var expiresAtVal any
	if expiresAt != "" {
		expiresAtVal = expiresAt
	}
	_, err := s.DB().Exec(
		`INSERT INTO feedback_requests(id, run_id, tool_name, proposed_input, message, status, expires_at, created_at)
		 VALUES (?, ?, ?, '{}', 'please respond', 'pending', ?, '2024-01-01T00:00:00Z')`,
		id, runID, toolName, expiresAtVal,
	)
	if err != nil {
		t.Fatalf("insertFeedbackRequest %s: %v", id, err)
	}
}

func pastTimestamp() string {
	return time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
}

func futureTimestamp() string {
	return time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339Nano)
}

// TestScanner_NonExpiredRequest verifies that a feedback request with expires_at
// in the future is not resolved by the scanner.
func TestScanner_NonExpiredRequest(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_feedback")
	insertFeedbackRequest(t, s, "f1", "r1", "ask_operator", futureTimestamp())

	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	var status string
	if err := s.DB().QueryRow(`SELECT status FROM feedback_requests WHERE id = 'f1'`).Scan(&status); err != nil {
		t.Fatalf("query feedback: %v", err)
	}
	if status != "pending" {
		t.Errorf("feedback status = %q, want pending (not yet expired)", status)
	}

	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "waiting_for_feedback" {
		t.Errorf("run status = %q, want waiting_for_feedback", runStatus)
	}
}

// TestScanner_NullExpiresAt verifies that a feedback request with NULL expires_at
// (no timeout configured) is never resolved by the scanner.
func TestScanner_NullExpiresAt(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_feedback")
	// Pass empty string to insertFeedbackRequest to produce NULL expires_at.
	insertFeedbackRequest(t, s, "f1", "r1", "ask_operator", "")

	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	var status string
	if err := s.DB().QueryRow(`SELECT status FROM feedback_requests WHERE id = 'f1'`).Scan(&status); err != nil {
		t.Fatalf("query feedback: %v", err)
	}
	if status != "pending" {
		t.Errorf("feedback status = %q, want pending (NULL expires_at must never time out)", status)
	}
}

// TestScanner_ExpiredRequest_MarksTimedOutAndFails verifies that an expired pending
// feedback request is resolved as timed_out and the run is marked failed.
func TestScanner_ExpiredRequest_MarksTimedOutAndFails(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_feedback")
	insertFeedbackRequest(t, s, "f1", "r1", "ask_operator", pastTimestamp())

	before := time.Now()
	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Feedback request must be marked timed_out with a resolved_at timestamp.
	var feedbackStatus, resolvedAt string
	if err := s.DB().QueryRow(`SELECT status, COALESCE(resolved_at, '') FROM feedback_requests WHERE id = 'f1'`).Scan(&feedbackStatus, &resolvedAt); err != nil {
		t.Fatalf("query feedback: %v", err)
	}
	if feedbackStatus != "timed_out" {
		t.Errorf("feedback status = %q, want timed_out", feedbackStatus)
	}
	if resolvedAt == "" {
		t.Error("resolved_at is empty, want a timestamp")
	} else {
		ts, err := time.Parse(time.RFC3339Nano, resolvedAt)
		if err != nil {
			t.Errorf("resolved_at %q not valid RFC3339Nano: %v", resolvedAt, err)
		} else if ts.Before(before) {
			t.Errorf("resolved_at %v is before test start %v", ts, before)
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

	// An error step must exist with feedback_timeout code.
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
	} else if payload["code"] != string(model.ErrorCodeFeedbackTimeout) {
		t.Errorf("step content code = %q, want %q", payload["code"], model.ErrorCodeFeedbackTimeout)
	}
}

// TestScanner_RunNotInWaitingForFeedback verifies that when the run is no longer
// in waiting_for_feedback (e.g. already interrupted), the feedback request is still
// marked timed_out but the run is not changed.
func TestScanner_RunNotInWaitingForFeedback(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	// Run was already moved to interrupted by ScanOrphanedRuns on restart.
	insertRun(t, s, "r1", "p1", "interrupted")
	insertFeedbackRequest(t, s, "f1", "r1", "ask_operator", pastTimestamp())

	scanner := NewScanner(s, time.Minute)
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Feedback must still be marked timed_out.
	var feedbackStatus string
	if err := s.DB().QueryRow(`SELECT status FROM feedback_requests WHERE id = 'f1'`).Scan(&feedbackStatus); err != nil {
		t.Fatalf("query feedback: %v", err)
	}
	if feedbackStatus != "timed_out" {
		t.Errorf("feedback status = %q, want timed_out", feedbackStatus)
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

// TestScanner_PublisherReceivesEvents verifies that run.status_changed and
// feedback.timed_out events are published when a feedback request times out.
func TestScanner_PublisherReceivesEvents(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_feedback")
	insertFeedbackRequest(t, s, "f1", "r1", "ask_operator", pastTimestamp())

	pub := &capturePublisher{}
	scanner := NewScanner(s, time.Minute, WithPublisher(pub))
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	types := pub.eventTypes()
	wantTypes := map[string]bool{
		"run.status_changed": false,
		"feedback.timed_out": false,
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

// TestScanner_StartStop verifies that the background goroutine fires and resolves
// an expired feedback request within a reasonable time.
func TestScanner_StartStop(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_feedback")
	insertFeedbackRequest(t, s, "f1", "r1", "ask_operator", pastTimestamp())

	ctx, cancel := context.WithCancel(context.Background())
	// Short interval so the scan fires quickly in the test.
	scanner := NewScanner(s, 20*time.Millisecond)
	scanner.Start(ctx)

	// Wait long enough for at least one scan tick.
	time.Sleep(100 * time.Millisecond)
	cancel()

	// The feedback request should have been resolved by now.
	var status string
	if err := s.DB().QueryRow(`SELECT status FROM feedback_requests WHERE id = 'f1'`).Scan(&status); err != nil {
		t.Fatalf("query feedback: %v", err)
	}
	if status != "timed_out" {
		t.Errorf("feedback status = %q after scanner ran, want timed_out", status)
	}
}

// TestScanner_StepNumberContinuesExistingSteps verifies that the error step
// created on timeout gets the correct step_number (CountRunSteps value).
func TestScanner_StepNumberContinuesExistingSteps(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "waiting_for_feedback")
	insertFeedbackRequest(t, s, "f1", "r1", "ask_operator", pastTimestamp())

	// Pre-insert 2 steps so the error step should get step_number = 2.
	_, err := s.DB().Exec(
		`INSERT INTO run_steps(id, run_id, step_number, type, content, created_at)
		 VALUES ('s1', 'r1', 0, 'thought', '{}', '2024-01-01T00:00:00Z'),
		        ('s2', 'r1', 1, 'feedback_request', '{}', '2024-01-01T00:00:00Z')`,
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

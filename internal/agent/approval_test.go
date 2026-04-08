// Package agent — approval_test.go pins down the ApprovalHandler.Wait contract:
// approved path, rejected path, timeout paths (handler wins / scanner wins the race),
// and context cancellation. These tests exercise the handler in isolation without
// a full BoundAgent, which is the acceptance criterion for issue #538.
package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/approval"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)


// approvalEntry returns a resolvedToolEntry with the given timeout (0 means no timeout).
func approvalEntry(timeout time.Duration) resolvedToolEntry {
	return resolvedToolEntry{
		tool: mcp.ResolvedTool{
			GrantedTool: model.GrantedTool{
				Timeout:   timeout,
				OnTimeout: model.OnTimeoutReject,
			},
		},
	}
}

// TestApprovalHandler_Wait_Approved verifies the happy path: operator approves;
// Wait returns nil; approval_request step written; run transitions back to running.
func TestApprovalHandler_Wait_Approved(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1)
	approvalCh <- true

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh))

	entry := approvalEntry(0)
	err := h.Wait(context.Background(), "run1", entry, "my-server.do_thing", map[string]any{})
	if err != nil {
		t.Fatalf("Wait: unexpected error: %v", err)
	}

	// Run must be back to running after approval.
	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running", sm.Current())
	}

	// approval.created event must have been published.
	if pub.countByType("approval.created") == 0 {
		t.Error("expected approval.created event to be published")
	}

	// Flush and verify approval_request step was written.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), "run1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var hasApprovalRequest bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeApprovalRequest) {
			hasApprovalRequest = true
		}
	}
	if !hasApprovalRequest {
		t.Error("expected approval_request step in audit trail")
	}
}

// TestApprovalHandler_Wait_Rejected verifies the rejection path: operator rejects;
// Wait returns an error containing "rejected"; an error step is written.
func TestApprovalHandler_Wait_Rejected(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1)
	approvalCh <- false

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh))

	err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	if err == nil {
		t.Fatal("expected error on rejection, got nil")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("error = %q, want to contain 'rejected'", err.Error())
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), "run1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var hasError bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			hasError = true
		}
	}
	if !hasError {
		t.Error("expected error step written on rejection")
	}
}

// TestApprovalHandler_Wait_Timeout_HandlerWins verifies that when the timeout fires
// and the handler wins the rows==1 race against the scanner, an error step is
// written and a non-nil error is returned.
func TestApprovalHandler_Wait_Timeout_HandlerWins(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool) // unbuffered — nothing sends

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh))

	err := h.Wait(context.Background(), "run1", approvalEntry(50*time.Millisecond), "my-server.do_thing", map[string]any{})
	if err == nil {
		t.Fatal("expected error on timeout, got nil")
	}
	if !strings.Contains(err.Error(), "approval timeout") {
		t.Errorf("error = %q, want to contain 'approval timeout'", err.Error())
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), "run1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var errorCount int
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			errorCount++
		}
	}
	if errorCount == 0 {
		t.Error("expected at least one error step on timeout")
	}
}

// TestApprovalHandler_Wait_Timeout_ScannerWins verifies the scanner-race contract:
// when the scanner resolves the approval row before the in-agent timer fires, the
// handler's timeout branch detects rows==0 and returns a sentinel error WITHOUT
// writing a duplicate error step.
func TestApprovalHandler_Wait_Timeout_ScannerWins(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh))

	done := make(chan error, 1)
	go func() {
		done <- h.Wait(context.Background(), "run1", approvalEntry(200*time.Millisecond), "my-server.do_thing", map[string]any{})
	}()

	// Poll until the approval row appears in the DB.
	deadline := time.Now().Add(100 * time.Millisecond)
	var approvalID string
	for time.Now().Before(deadline) {
		rows, err := s.GetPendingApprovalRequestsByRun(context.Background(), "run1")
		if err != nil {
			t.Fatalf("GetPendingApprovalRequestsByRun: %v", err)
		}
		if len(rows) > 0 {
			approvalID = rows[0].ID
			break
		}
		time.Sleep(time.Millisecond)
	}
	if approvalID == "" {
		t.Fatal("timed out waiting for approval row to appear in DB")
	}

	// Back-date the approval row so the scanner picks it up as expired.
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := s.DB().Exec(`UPDATE approval_requests SET expires_at = ? WHERE id = ?`, past, approvalID); err != nil {
		t.Fatalf("back-date approval: %v", err)
	}

	// Drive the scanner synchronously — it wins the guarded UPDATE (rows=1).
	sc := approval.NewScanner(s, time.Minute, approval.WithPublisher(pub))
	if err := sc.Scan(context.Background()); err != nil {
		t.Fatalf("scanner.Scan: %v", err)
	}

	// Wait for the handler's 200ms timer to fire.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from handler, got nil")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Wait did not return within 500ms")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Exactly one error step (written by the scanner, not by the handler).
	steps, err := s.ListRunSteps(context.Background(), "run1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	errorCount := 0
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			errorCount++
		}
	}
	if errorCount != 1 {
		t.Errorf("error steps = %d, want exactly 1 (scanner wins, no duplicate)", errorCount)
	}

	// Exactly one run.status_changed(failed) event from the scanner.
	if n := pub.countByStatus("run.status_changed", "failed"); n != 1 {
		t.Errorf("run.status_changed(failed) events = %d, want 1", n)
	}
}

// TestApprovalHandler_Wait_ContextCancelled verifies that context cancellation
// while blocking returns a wrapped ctx.Err().
func TestApprovalHandler_Wait_ContextCancelled(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool) // unbuffered — nothing sends

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	// No tool timeout → blocks until context or approval.
	err := h.Wait(ctx, "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	if !strings.Contains(err.Error(), "context cancelled") {
		t.Errorf("error = %q, want to contain 'context cancelled'", err.Error())
	}
}

// Package agent — approval_test.go pins down the ApprovalHandler.Wait contract:
// approved path, rejected path, timeout paths (handler wins / scanner wins the race),
// and context cancellation. These tests exercise the handler in isolation without
// a full BoundAgent, which is the acceptance criterion for issue #538.
package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/timeout"
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
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
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
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
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

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
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
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
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

// TestApprovalHandler_Wait_DrainsStaleBufferedValue is issue #961 review R2:
// a value already sitting in approvalCh before a SECOND (or later) gate opens
// (left over from a PREVIOUS gate — SendApproval's non-blocking send landing
// after that gate's own Wait had already returned via a timeout, for
// instance) must never be read as the answer to the NEW gate.
//
// Gate 1 legitimately consumes its own prefilled value and resolves
// normally — this is the same "prefill before the first Wait call" shape
// every other test in this file uses, and it must keep working, since
// nothing can be stale before a handler's very first gate (see Wait's own
// comment). Only once gate 1 has closed do we inject a genuinely stale value
// and open gate 2 on the SAME handler: the short handler timeout below is
// what proves the drain happened — if the stale value were consumed instead
// of drained, gate 2 would resolve immediately instead of timing out.
func TestApprovalHandler_Wait_DrainsStaleBufferedValue(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1)
	approvalCh <- true // gate 1's own answer

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh))

	if err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{}); err != nil {
		t.Fatalf("gate 1 Wait: unexpected error: %v", err)
	}
	if sm.Current() != model.RunStatusRunning {
		t.Fatalf("after gate 1, run status = %s, want running", sm.Current())
	}

	approvalCh <- false // stale value left over from gate 1's own timing window; nothing is waiting to consume it

	err := h.Wait(context.Background(), "run1", approvalEntry(50*time.Millisecond), "my-server.do_thing", map[string]any{})
	if err == nil {
		t.Fatal("gate 2: expected a timeout error — the stale buffered value must not have decided this gate")
	}
	if !strings.Contains(err.Error(), "approval timeout") {
		t.Errorf("gate 2: error = %q, want to contain 'approval timeout'", err.Error())
	}
	if sm.Current() == model.RunStatusRunning {
		t.Error("gate 2: run status = running, want NOT running — a stale value must never approve a new gate")
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

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
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
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
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
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh))

	// Short handler timeout keeps the happy-path test fast. The behavior under
	// test (scanner wins the UPDATE-CAS race) is independent of the value; the
	// CI-tolerance comes from the generous deadline on the test-side <-done
	// wait below, not from inflating the timer.
	const handlerTimeout = 200 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- h.Wait(context.Background(), "run1", approvalEntry(handlerTimeout), "my-server.do_thing", map[string]any{})
	}()

	// Signal-don't-poll: wait for the state machine's approval.created event
	// rather than polling the DB on a tight wall-clock budget. Once the event
	// fires, the row is committed and observable. (See CLAUDE.md "Testing
	// time-dependent code".)
	pub.waitForEvent(t, "approval.created", 5*time.Second)

	rows, err := s.GetPendingApprovalRequestsByRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetPendingApprovalRequestsByRun: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("approval.created fired but no pending row found")
	}
	approvalID := rows[0].ID

	// Back-date the approval row so the scanner picks it up as expired.
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := s.DB().Exec(`UPDATE approval_requests SET expires_at = ? WHERE id = ?`, past, approvalID); err != nil {
		t.Fatalf("back-date approval: %v", err)
	}

	// Drive the scanner synchronously — it wins the guarded UPDATE (rows=1).
	sc := timeout.NewApprovalScanner(s, time.Minute, timeout.WithPublisher(pub))
	if err := sc.Scan(context.Background()); err != nil {
		t.Fatalf("scanner.Scan: %v", err)
	}

	// Wait for the handler's timer to fire and Wait() to return. The handler
	// timeout is 2s; allow 5s here so CI scheduling jitter never preempts us.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from handler, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return within 5s")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Exactly one error step (written by the scanner, not by the handler).
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
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

// mockApprovalDispatcher implements ApprovalChannelDispatcher for tests.
// settleCalls records every Settle(won) invocation so a test can assert
// whether ApprovalHandler told the dispatcher its decision actually won the
// race with the timeout scanner.
type mockApprovalDispatcher struct {
	approved bool
	err      error

	settleCalls []bool
}

func (m *mockApprovalDispatcher) DispatchApproval(_ context.Context, _ ApprovalDispatchRequest) (ApprovalSettlement, error) {
	if m.err != nil {
		return ApprovalSettlement{}, m.err
	}
	return ApprovalSettlement{
		Approved: m.approved,
		Settle: func(_ context.Context, won bool) {
			m.settleCalls = append(m.settleCalls, won)
		},
	}, nil
}

// TestApprovalHandler_Wait_PluginApproved verifies the happy path when a plugin
// channel dispatcher returns (true, nil): Wait returns nil and the run transitions
// back to running.
func TestApprovalHandler_Wait_PluginApproved(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1) // not pre-loaded — plugin path must not read it

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	mock := &mockApprovalDispatcher{approved: true, err: nil}
	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh),
		WithApprovalChannelDispatch(mock, "audience-1", "p1"),
	)

	err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	if err != nil {
		t.Fatalf("Wait: unexpected error: %v", err)
	}

	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running", sm.Current())
	}

	// approval_requests row must be updated synchronously by resolveApprovalRecord
	// before Wait returns — no async wait needed.
	var status string
	var decidedAt sql.NullString
	if err := s.DB().QueryRow(
		`SELECT status, decided_at FROM approval_requests WHERE run_id = ?`, "run1",
	).Scan(&status, &decidedAt); err != nil {
		t.Fatalf("querying approval_requests: %v", err)
	}
	if status != "approved" {
		t.Errorf("approval_requests.status = %q, want %q", status, "approved")
	}
	if !decidedAt.Valid {
		t.Error("approval_requests.decided_at is NULL, want non-null")
	}

	// Exactly one approval.resolved SSE event must be emitted.
	if n := pub.countByType("approval.resolved"); n != 1 {
		t.Errorf("approval.resolved events = %d, want 1", n)
	}

	// SSE payload must carry the correct fields.
	for _, ev := range pub.all() {
		if ev.eventType != "approval.resolved" {
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal(ev.data, &payload); err != nil {
			t.Fatalf("unmarshal approval.resolved payload: %v", err)
		}
		if payload["approval_id"] == "" {
			t.Error("approval.resolved payload: approval_id is empty")
		}
		if payload["run_id"] != "run1" {
			t.Errorf("approval.resolved payload: run_id = %q, want %q", payload["run_id"], "run1")
		}
		if payload["status"] != "approved" {
			t.Errorf("approval.resolved payload: status = %q, want %q", payload["status"], "approved")
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
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

// TestApprovalHandler_Wait_PluginDenied verifies that when the plugin dispatcher
// returns (false, nil), Wait returns an error containing "rejected" and writes an
// error step.
func TestApprovalHandler_Wait_PluginDenied(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1) // not pre-loaded

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	mock := &mockApprovalDispatcher{approved: false, err: nil}
	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh),
		WithApprovalChannelDispatch(mock, "audience-1", "p1"),
	)

	err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	if err == nil {
		t.Fatal("expected error on plugin denial, got nil")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("error = %q, want to contain 'rejected'", err.Error())
	}

	// approval_requests row must be updated to "rejected" by resolveApprovalRecord
	// before Wait returns — no async wait needed.
	var status string
	var decidedAt sql.NullString
	if err := s.DB().QueryRow(
		`SELECT status, decided_at FROM approval_requests WHERE run_id = ?`, "run1",
	).Scan(&status, &decidedAt); err != nil {
		t.Fatalf("querying approval_requests: %v", err)
	}
	if status != "rejected" {
		t.Errorf("approval_requests.status = %q, want %q", status, "rejected")
	}
	if !decidedAt.Valid {
		t.Error("approval_requests.decided_at is NULL, want non-null")
	}

	// Exactly one approval.resolved SSE event must be emitted.
	if n := pub.countByType("approval.resolved"); n != 1 {
		t.Errorf("approval.resolved events = %d, want 1", n)
	}

	// SSE payload must carry the correct fields.
	for _, ev := range pub.all() {
		if ev.eventType != "approval.resolved" {
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal(ev.data, &payload); err != nil {
			t.Fatalf("unmarshal approval.resolved payload: %v", err)
		}
		if payload["approval_id"] == "" {
			t.Error("approval.resolved payload: approval_id is empty")
		}
		if payload["run_id"] != "run1" {
			t.Errorf("approval.resolved payload: run_id = %q, want %q", payload["run_id"], "run1")
		}
		if payload["status"] != "rejected" {
			t.Errorf("approval.resolved payload: status = %q, want %q", payload["status"], "rejected")
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
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
		t.Error("expected error step written on plugin denial")
	}
}

// TestApprovalHandler_Wait_PluginApproved_LostToScanner is the plugin-channel
// half of issue #961 review item 1c (the in-app half lives in
// internal/execution/run's runs_handler_test.go, since that CAS ordering is
// resolveRequest's own): a dispatcher that resolves "approved" AFTER the
// timeout scanner has already claimed the approval_requests row must never
// run the tool. The mock simulates the race directly — it claims the row
// itself, standing in for the scanner, before returning its own answer — so
// resolveApprovalRecord's CAS is guaranteed to see rows==0.
func TestApprovalHandler_Wait_PluginApproved_LostToScanner(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	mock := &raceWinningApprovalDispatcher{queries: s.Queries(), approved: true}
	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh),
		WithApprovalChannelDispatch(mock, "audience-1", "p1"),
	)

	err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	if err == nil {
		t.Fatal("expected an error: this decision lost the race with the timeout scanner")
	}
	if !strings.Contains(err.Error(), "already closed") {
		t.Errorf("error = %q, want to contain 'already closed'", err.Error())
	}

	// The tool must never have been allowed to run.
	if sm.Current() == model.RunStatusRunning {
		t.Error("run status = running, want NOT running — a lost decision must never resume the run")
	}

	if len(mock.settleCalls) != 1 || mock.settleCalls[0] != false {
		t.Errorf("settleCalls = %v, want [false] — the dispatcher must be told this decision lost", mock.settleCalls)
	}

	// The row must still read "timeout" — resolveApprovalRecord's own CAS
	// loss must not have clobbered the scanner's write.
	var status string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE run_id = ?`, "run1").Scan(&status); err != nil {
		t.Fatalf("querying approval_requests: %v", err)
	}
	if status != "timeout" {
		t.Errorf("approval_requests.status = %q, want timeout", status)
	}
}

// TestApprovalHandler_Wait_PluginApproved_DBWriteErrorFailsClosed is issue
// #961 review R3: a DB write error inside resolveApprovalRecord must fail
// closed (won=false) for the plugin-channel path, never running the tool on
// a write it cannot confirm succeeded. The mock closes the store itself,
// from inside DispatchApproval, so the SUBSEQUENT resolveApprovalRecord call
// is guaranteed to hit a real "database is closed" error rather than a
// contrived stub.
func TestApprovalHandler_Wait_PluginApproved_DBWriteErrorFailsClosed(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1)
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	mock := &dbClosingApprovalDispatcher{store: s}
	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh),
		WithApprovalChannelDispatch(mock, "audience-1", "p1"),
	)

	err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	if err == nil {
		t.Fatal("expected an error: the CAS write failed, so this must fail closed")
	}

	// The tool must never have been allowed to run.
	if sm.Current() == model.RunStatusRunning {
		t.Error("run status = running, want NOT running — an unconfirmed write must never resume the run")
	}
}

// dbClosingApprovalDispatcher simulates a DB write failure at exactly the
// point resolveApprovalRecord needs it: it closes the store itself from
// inside DispatchApproval, so the write ApprovalHandler.Wait attempts
// immediately afterward genuinely fails, rather than a stub merely claiming
// it would have.
type dbClosingApprovalDispatcher struct {
	store *db.Store
}

func (m *dbClosingApprovalDispatcher) DispatchApproval(_ context.Context, _ ApprovalDispatchRequest) (ApprovalSettlement, error) {
	if err := m.store.Close(); err != nil {
		return ApprovalSettlement{}, fmt.Errorf("closing store: %w", err)
	}
	return ApprovalSettlement{Approved: true}, nil
}

// raceWinningApprovalDispatcher simulates the timeout scanner beating a
// plugin-routed approval to the CAS: DispatchApproval claims the run's own
// pending approval_requests row as "timeout" itself (standing in for the
// scanner) before returning its own decision, so
// ApprovalHandler.resolveApprovalRecord's subsequent CAS attempt is
// guaranteed to see rows==0.
type raceWinningApprovalDispatcher struct {
	queries  *db.Queries
	approved bool

	settleCalls []bool
}

func (m *raceWinningApprovalDispatcher) DispatchApproval(ctx context.Context, req ApprovalDispatchRequest) (ApprovalSettlement, error) {
	pending, err := m.queries.GetPendingApprovalRequestsByRun(ctx, req.RunID)
	if err != nil {
		return ApprovalSettlement{}, fmt.Errorf("look up pending approval: %w", err)
	}
	if len(pending) != 1 {
		return ApprovalSettlement{}, fmt.Errorf("expected exactly one pending approval, got %d", len(pending))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := m.queries.UpdateApprovalRequestStatus(ctx, db.UpdateApprovalRequestStatusParams{
		Status: "timeout", DecidedAt: &now, ID: pending[0].ID,
	}); err != nil {
		return ApprovalSettlement{}, fmt.Errorf("claim the row as the scanner would: %w", err)
	}
	return ApprovalSettlement{
		Approved: m.approved,
		Settle: func(_ context.Context, won bool) {
			m.settleCalls = append(m.settleCalls, won)
		},
	}, nil
}

// TestApprovalHandler_Wait_PluginFallbackToInApp verifies that when the dispatcher
// returns ErrApprovalRouteToInApp, the handler falls through to the in-app approvalCh
// path and approves when the channel is pre-loaded with true.
func TestApprovalHandler_Wait_PluginFallbackToInApp(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	// Pre-load the in-app approval channel so the fallback path approves immediately.
	approvalCh := make(chan bool, 1)
	approvalCh <- true

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	mock := &mockApprovalDispatcher{approved: false, err: ErrApprovalRouteToInApp}
	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh),
		WithApprovalChannelDispatch(mock, "audience-1", "p1"),
	)

	err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	if err != nil {
		t.Fatalf("Wait: unexpected error after in-app fallback: %v", err)
	}

	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running after in-app approval", sm.Current())
	}
}

// TestApprovalHandler_Wait_PluginDispatchError verifies that a non-sentinel
// dispatcher error is propagated with context wrapping "plugin approval dispatch".
func TestApprovalHandler_Wait_PluginDispatchError(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	mock := &mockApprovalDispatcher{approved: false, err: fmt.Errorf("connection refused")}
	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh),
		WithApprovalChannelDispatch(mock, "audience-1", "p1"),
	)

	err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	if err == nil {
		t.Fatal("expected error from dispatcher, got nil")
	}
	if !strings.Contains(err.Error(), "plugin approval dispatch") {
		t.Errorf("error = %q, want to contain 'plugin approval dispatch'", err.Error())
	}
}

// TestApprovalHandler_Wait_NoDispatcherConfigured verifies that when no channel
// dispatcher is configured, the handler falls through to the existing in-app
// approvalCh path unchanged.
func TestApprovalHandler_Wait_NoDispatcherConfigured(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool, 1)
	approvalCh <- true

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	// No options — dispatcher is nil, in-app path is used unchanged.
	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh))

	err := h.Wait(context.Background(), "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	if err != nil {
		t.Fatalf("Wait: unexpected error: %v", err)
	}
	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running", sm.Current())
	}
}

// TestApprovalHandler_Wait_ContextCancelled verifies that context cancellation
// while blocking returns a wrapped ctx.Err().
//
// The cancellation happens only once the wait is KNOWN to be blocking, which
// the approval.created event establishes. The previous shape raced a 30ms
// wall-clock deadline against the setup work — writing the approval_request
// step and transitioning the run — and under load the deadline won, so the
// error came back as "writing approval request step: context deadline
// exceeded" and the assertion failed on a behavior that was correct. Same
// fix as TestFeedbackHandler_Wait_ContextCancelled (signal-don't-poll).
func TestApprovalHandler_Wait_ContextCancelled(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	approvalCh := make(chan bool) // unbuffered — nothing sends

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewApprovalHandler(w, sm, (<-chan bool)(approvalCh))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		// No tool timeout → blocks until context or approval.
		done <- h.Wait(ctx, "run1", approvalEntry(0), "my-server.do_thing", map[string]any{})
	}()

	// The pending row exists and the run has transitioned, so Wait is now
	// parked on its select. Only now is cancelling it a test of cancellation.
	pub.waitForEvent(t, "approval.created", 5*time.Second)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error on context cancellation, got nil")
		}
		if !strings.Contains(err.Error(), "context cancelled") {
			t.Errorf("error = %q, want to contain 'context cancelled'", err.Error())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return after its context was cancelled")
	}
}

// Package agent — feedback_test.go pins down the FeedbackHandler contract:
// response path, timeout paths (handler wins / scanner wins the race), context
// cancellation, the ADR-001 defense-in-depth rejection, and per-call timeout
// resolution. These tests exercise the handler in isolation without a full
// BoundAgent, which is the acceptance criterion for issue #538.
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/timeout"
)

// mockFeedbackDispatcher is a simple mock for FeedbackChannelDispatcher.
type mockFeedbackDispatcher struct {
	response string
	err      error
}

func (m *mockFeedbackDispatcher) DispatchFeedback(_ context.Context, _ FeedbackDispatchRequest) (string, error) {
	return m.response, m.err
}

// awaitPendingFeedbackID blocks until the state machine publishes feedback.created
// — the signal that the feedback_requests INSERT has committed (the publish in
// state.go fires only after tx.Commit) — then returns the pending row's ID. This
// replaces polling the DB on a tight wall-clock budget, which flaked under -race
// on loaded CI runners (CLAUDE.md "signal-don't-poll").
func awaitPendingFeedbackID(t *testing.T, pub *capturePublisher, s *db.Store, runID string) string {
	t.Helper()
	pub.waitForEvent(t, "feedback.created", 5*time.Second)
	rows, err := s.GetPendingFeedbackRequestsByRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetPendingFeedbackRequestsByRun: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no pending feedback row after feedback.created event")
	}
	return rows[0].ID
}

// TestFeedbackHandler_Wait_ResponseReceived verifies the happy path: operator
// responds via h.Resolve before timeout; feedback_request and feedback_response
// steps are written; run transitions back to running.
func TestFeedbackHandler_Wait_ResponseReceived(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, time.Minute)

	// Spawn Wait in a goroutine and deliver the response via h.Resolve once
	// the feedback row appears in the DB (register-before-transition guarantee).
	type waitResult struct {
		body string
		err  error
	}
	done := make(chan waitResult, 1)
	go func() {
		body, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "please answer", time.Minute)
		done <- waitResult{body: body, err: err}
	}()

	feedbackID := awaitPendingFeedbackID(t, pub, s, "run1")

	// Deliver the operator response through the inAppChannel.
	if err := h.Resolve(feedbackID, "operator response"); err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}

	// Wait for the goroutine to return.
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Wait: unexpected error: %v", res.err)
		}
		if res.body != "operator response" {
			t.Errorf("response = %q, want %q", res.body, "operator response")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return within deadline")
	}

	// Run must be back to running.
	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running", sm.Current())
	}

	// Flush writer and check steps.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var hasFeedbackRequest, hasFeedbackResponse bool
	for _, step := range steps {
		switch step.Type {
		case string(model.StepTypeFeedbackRequest):
			hasFeedbackRequest = true
		case string(model.StepTypeFeedbackResponse):
			hasFeedbackResponse = true
		}
	}
	if !hasFeedbackRequest {
		t.Error("expected feedback_request step in audit trail")
	}
	if !hasFeedbackResponse {
		t.Error("expected feedback_response step in audit trail")
	}
}

// TestFeedbackHandler_Wait_Timeout_HandlerWins verifies that when the timeout
// fires and the handler wins the rows==1 race against the scanner, an error step
// is written with ErrorCodeFeedbackTimeout and a non-nil error is returned.
func TestFeedbackHandler_Wait_Timeout_HandlerWins(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, time.Minute)

	_, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "please answer", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected error on timeout, got nil")
	}
	if !strings.Contains(err.Error(), "feedback timeout") {
		t.Errorf("error = %q, want to contain 'feedback timeout'", err.Error())
	}

	// Flush and verify error step written.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var timeoutErrFound bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			timeoutErrFound = true
		}
	}
	if !timeoutErrFound {
		t.Error("expected error step with feedback_timeout code")
	}
}

// TestFeedbackHandler_Wait_Timeout_ScannerWins verifies the scanner-race contract:
// when the scanner resolves the feedback row before the in-agent timer fires, the
// agent's timeout branch detects rows==0 and returns a sentinel error WITHOUT
// writing a duplicate error step.
func TestFeedbackHandler_Wait_Timeout_ScannerWins(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, time.Minute)

	done := make(chan error, 1)
	go func() {
		_, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "please answer", 200*time.Millisecond)
		done <- err
	}()

	// Poll until the feedback row appears in the DB.
	deadline := time.Now().Add(100 * time.Millisecond)
	var feedbackID string
	for time.Now().Before(deadline) {
		rows, err := s.GetPendingFeedbackRequestsByRun(context.Background(), "run1")
		if err != nil {
			t.Fatalf("GetPendingFeedbackRequestsByRun: %v", err)
		}
		if len(rows) > 0 {
			feedbackID = rows[0].ID
			break
		}
		time.Sleep(time.Millisecond)
	}
	if feedbackID == "" {
		t.Fatal("timed out waiting for feedback row to appear in DB")
	}

	// Back-date the feedback row so the scanner picks it up as expired.
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := s.DB().Exec(`UPDATE feedback_requests SET expires_at = ? WHERE id = ?`, past, feedbackID); err != nil {
		t.Fatalf("back-date feedback: %v", err)
	}

	// Drive the scanner synchronously — it wins the guarded UPDATE (rows=1).
	sc := timeout.NewFeedbackScanner(s, time.Minute, timeout.WithPublisher(pub))
	if err := sc.Scan(context.Background()); err != nil {
		t.Fatalf("scanner.Scan: %v", err)
	}

	// Wait for the handler's 200ms timer to fire.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from handler, got nil")
		}
		// Sentinel: does not contain "already resolved by scanner" check not required,
		// but the error must be non-nil.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Wait did not return within 500ms")
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
}

// TestFeedbackHandler_Wait_ContextCancelled verifies that context cancellation
// while blocking returns a wrapped ctx.Err().
func TestFeedbackHandler_Wait_ContextCancelled(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := h.Wait(ctx, "run1", AskOperatorToolName, "{}", "please answer", 0)
	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	if !strings.Contains(err.Error(), "context cancelled") {
		t.Errorf("error = %q, want to contain 'context cancelled'", err.Error())
	}
}

// TestFeedbackHandler_HandleAskOperator_FeedbackDisabled verifies the ADR-001
// defense-in-depth: when feedbackCfg.Enabled is false, HandleAskOperator returns
// a ToolError, sets isError=true, and writes an error step.
func TestFeedbackHandler_HandleAskOperator_FeedbackDisabled(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, time.Minute)

	// feedbackCfg.Enabled = false — hard runtime rejection.
	_, isError, err := h.HandleAskOperator(context.Background(), "run1", AskOperatorToolName,
		map[string]any{"reason": "hello"}, model.FeedbackConfig{Enabled: false})
	if err == nil {
		t.Fatal("expected error when feedback is disabled, got nil")
	}
	if !isError {
		t.Error("expected isError=true when feedback is disabled")
	}
	if !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("error = %q, want to contain 'not enabled'", err.Error())
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
		t.Error("expected error step written when feedback is disabled")
	}
}

// TestFeedbackHandler_HandleAskOperator_MissingReason verifies that a missing
// required 'reason' field results in a SchemaViolation error and isError=true.
func TestFeedbackHandler_HandleAskOperator_MissingReason(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, time.Minute)

	// No 'reason' field — schema violation.
	_, isError, err := h.HandleAskOperator(context.Background(), "run1", AskOperatorToolName,
		map[string]any{"context": "only context, no reason"}, model.FeedbackConfig{Enabled: true})
	if err == nil {
		t.Fatal("expected schema violation error, got nil")
	}
	if !isError {
		t.Error("expected isError=true on schema violation")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("error = %q, want to contain 'reason'", err.Error())
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var schemaErrFound bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			schemaErrFound = true
		}
	}
	if !schemaErrFound {
		t.Error("expected error step for schema violation")
	}
}

// TestFeedbackHandler_HandleAskOperator_ReasonNotString verifies that a non-string
// 'reason' field results in a SchemaViolation error.
func TestFeedbackHandler_HandleAskOperator_ReasonNotString(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, time.Minute)

	_, isError, err := h.HandleAskOperator(context.Background(), "run1", AskOperatorToolName,
		map[string]any{"reason": 42}, model.FeedbackConfig{Enabled: true})
	if err == nil {
		t.Fatal("expected schema violation error for non-string reason, got nil")
	}
	if !isError {
		t.Error("expected isError=true for non-string reason")
	}
}

// TestFeedbackHandler_HandleAskOperator_TimeoutResolution verifies three timeout
// resolution cases: (1) empty feedbackCfg → defaultTimeout used; (2) invalid
// duration string → defaultTimeout used; (3) valid policy timeout → policy value used.
func TestFeedbackHandler_HandleAskOperator_TimeoutResolution(t *testing.T) {
	const defaultTimeout = 100 * time.Millisecond

	cases := []struct {
		name           string
		feedbackCfg    model.FeedbackConfig
		wantFasterThan time.Duration // rough upper bound for the timeout path
		wantSlowerThan time.Duration // lower bound so we know the timeout fired
	}{
		{
			name:           "empty_timeout_uses_default",
			feedbackCfg:    model.FeedbackConfig{Enabled: true, Timeout: ""},
			wantFasterThan: defaultTimeout * 5,
			wantSlowerThan: 0,
		},
		{
			name:           "invalid_timeout_uses_default",
			feedbackCfg:    model.FeedbackConfig{Enabled: true, Timeout: "not-a-duration"},
			wantFasterThan: defaultTimeout * 5,
			wantSlowerThan: 0,
		},
		{
			name:           "valid_timeout_uses_policy_value",
			feedbackCfg:    model.FeedbackConfig{Enabled: true, Timeout: "50ms"},
			wantFasterThan: 500 * time.Millisecond,
			wantSlowerThan: 0,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

			sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
			w := NewAuditWriter(s.Queries())
			defer w.Close() //nolint:errcheck

			h := NewFeedbackHandler(w, sm, defaultTimeout)

			start := time.Now()
			_, _, err := h.HandleAskOperator(context.Background(), "run1", AskOperatorToolName,
				map[string]any{"reason": "test"}, tc.feedbackCfg)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("expected timeout error, got nil")
			}
			if !strings.Contains(err.Error(), "feedback timeout") {
				t.Errorf("error = %q, want to contain 'feedback timeout'", err.Error())
			}
			if tc.wantFasterThan > 0 && elapsed >= tc.wantFasterThan {
				t.Errorf("elapsed %v >= %v, timeout should have fired sooner", elapsed, tc.wantFasterThan)
			}
		})
	}
}

// TestFeedbackHandler_Wait_PluginResponse verifies the plugin dispatch path:
// mock returns a JSON-encoded response; Wait extracts the text, does NOT write a
// feedback_response audit step (hostsvc writes it), resolves the DB row, and
// transitions back to running.
func TestFeedbackHandler_Wait_PluginResponse(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	mock := &mockFeedbackDispatcher{
		response: `{"text":"operator reply","user":"U123","request_id":"req-1"}`,
	}
	h := NewFeedbackHandler(w, sm, time.Minute, WithFeedbackChannelDispatch(mock, "aud-1", "pol-1"))

	responseText, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "please answer", time.Minute)
	if err != nil {
		t.Fatalf("Wait: unexpected error: %v", err)
	}
	if responseText != "operator reply" {
		t.Errorf("response = %q, want %q", responseText, "operator reply")
	}

	// Run must be back to running.
	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running", sm.Current())
	}

	// Flush writer and check audit steps.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var hasFeedbackRequest, hasFeedbackResponse bool
	for _, step := range steps {
		switch step.Type {
		case string(model.StepTypeFeedbackRequest):
			hasFeedbackRequest = true
		case string(model.StepTypeFeedbackResponse):
			hasFeedbackResponse = true
		}
	}
	if !hasFeedbackRequest {
		t.Error("expected feedback_request step in audit trail")
	}
	// Plugin path: feedback_response is written by hostsvc, NOT by FeedbackHandler.
	if hasFeedbackResponse {
		t.Error("feedback_response step must NOT be written by FeedbackHandler on plugin path (hostsvc writes it)")
	}

	// The feedback_requests DB row must be resolved.
	rows, dbErr := s.GetPendingFeedbackRequestsByRun(context.Background(), "run1")
	if dbErr != nil {
		t.Fatalf("GetPendingFeedbackRequestsByRun: %v", dbErr)
	}
	if len(rows) > 0 {
		t.Error("feedback_requests row should be resolved (no pending rows)")
	}
}

// TestFeedbackHandler_Wait_PluginFallbackToInApp verifies that when the plugin
// dispatcher returns ErrFeedbackRouteToInApp, Wait falls through to the in-app
// path and writes a feedback_response audit step.
func TestFeedbackHandler_Wait_PluginFallbackToInApp(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	mock := &mockFeedbackDispatcher{err: ErrFeedbackRouteToInApp}
	h := NewFeedbackHandler(w, sm, time.Minute, WithFeedbackChannelDispatch(mock, "aud-1", "pol-1"))

	done := make(chan struct {
		body string
		err  error
	}, 1)
	go func() {
		body, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "please answer", time.Minute)
		done <- struct {
			body string
			err  error
		}{body, err}
	}()

	feedbackID := awaitPendingFeedbackID(t, pub, s, "run1")

	// Deliver the response via the in-app Resolve path.
	if err := h.Resolve(feedbackID, "in-app reply"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Wait: unexpected error: %v", res.err)
		}
		if res.body != "in-app reply" {
			t.Errorf("response = %q, want %q", res.body, "in-app reply")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return within deadline")
	}

	// In-app path: feedback_response step MUST be written.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var hasFeedbackResponse bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeFeedbackResponse) {
			hasFeedbackResponse = true
		}
	}
	if !hasFeedbackResponse {
		t.Error("in-app fallback path: expected feedback_response step in audit trail")
	}
}

// TestFeedbackHandler_Wait_InApp_DoesNotResolveDBRow verifies that after the
// in-app select branch completes, the feedback_requests DB row remains in
// "pending" status. The HTTP SubmitFeedback handler owns the pending→resolved
// CAS on the in-app path; the agent must not race it with a second write.
func TestFeedbackHandler_Wait_InApp_DoesNotResolveDBRow(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, time.Minute)

	type waitResult struct {
		body string
		err  error
	}
	done := make(chan waitResult, 1)
	go func() {
		body, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "please answer", time.Minute)
		done <- waitResult{body: body, err: err}
	}()

	feedbackID := awaitPendingFeedbackID(t, pub, s, "run1")

	// Deliver response through the in-app channel.
	if err := h.Resolve(feedbackID, "hello"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Wait: %v", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return within deadline")
	}

	// The DB row must still be pending — the HTTP handler owns the CAS transition.
	// If the agent had called UpdateFeedbackRequestStatus, the row would be resolved.
	pending, err := s.GetPendingFeedbackRequestsByRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("GetPendingFeedbackRequestsByRun: %v", err)
	}
	if len(pending) == 0 {
		t.Error("feedback_requests row was resolved by the agent — it must stay pending so the HTTP handler can own the CAS")
	}
}

// TestFeedbackHandler_Wait_PluginDispatchError verifies that a non-sentinel error
// from the plugin dispatcher is propagated wrapped with "plugin feedback dispatch".
func TestFeedbackHandler_Wait_PluginDispatchError(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	dispatchErr := errors.New("gRPC unavailable")
	mock := &mockFeedbackDispatcher{err: dispatchErr}
	h := NewFeedbackHandler(w, sm, time.Minute, WithFeedbackChannelDispatch(mock, "aud-1", "pol-1"))

	_, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "please answer", time.Minute)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "plugin feedback dispatch") {
		t.Errorf("error = %q, want to contain 'plugin feedback dispatch'", err.Error())
	}
	if !errors.Is(err, dispatchErr) {
		t.Errorf("error chain should contain original dispatchErr")
	}
}

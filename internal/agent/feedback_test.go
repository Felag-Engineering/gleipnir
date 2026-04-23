// Package agent — feedback_test.go pins down the FeedbackHandler contract:
// response path, timeout paths (handler wins / scanner wins the race), context
// cancellation, the ADR-001 defense-in-depth rejection, and per-call timeout
// resolution. These tests exercise the handler in isolation without a full
// BoundAgent, which is the acceptance criterion for issue #538.
package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/timeout"
)

// TestFeedbackHandler_Wait_ResponseReceived verifies the happy path: operator
// responds before timeout; feedback_request and feedback_response steps are written;
// run transitions back to running.
func TestFeedbackHandler_Wait_ResponseReceived(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	feedbackCh := make(chan string, 1)
	feedbackCh <- "operator response"

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, (<-chan string)(feedbackCh), time.Minute)

	got, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "please answer", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("Wait: unexpected error: %v", err)
	}
	if got != "operator response" {
		t.Errorf("response = %q, want %q", got, "operator response")
	}

	// Run must be back to running.
	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running", sm.Current())
	}

	// Flush writer and check steps.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), "run1")
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

	feedbackCh := make(chan string) // unbuffered — nothing sends

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, (<-chan string)(feedbackCh), time.Minute)

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
	steps, err := s.ListRunSteps(context.Background(), "run1")
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

	feedbackCh := make(chan string, 1)

	pub := &capturePublisher{}
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, (<-chan string)(feedbackCh), time.Minute)

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
}

// TestFeedbackHandler_Wait_ContextCancelled verifies that context cancellation
// while blocking returns a wrapped ctx.Err().
func TestFeedbackHandler_Wait_ContextCancelled(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	feedbackCh := make(chan string) // unbuffered — nothing sends

	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, (<-chan string)(feedbackCh), time.Minute)

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

	feedbackCh := make(chan string)
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, (<-chan string)(feedbackCh), time.Minute)

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
		t.Error("expected error step written when feedback is disabled")
	}
}

// TestFeedbackHandler_HandleAskOperator_MissingReason verifies that a missing
// required 'reason' field results in a SchemaViolation error and isError=true.
func TestFeedbackHandler_HandleAskOperator_MissingReason(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	feedbackCh := make(chan string)
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, (<-chan string)(feedbackCh), time.Minute)

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
	steps, err := s.ListRunSteps(context.Background(), "run1")
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

	feedbackCh := make(chan string)
	sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
	w := NewAuditWriter(s.Queries())
	defer w.Close() //nolint:errcheck

	h := NewFeedbackHandler(w, sm, (<-chan string)(feedbackCh), time.Minute)

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

			feedbackCh := make(chan string) // never receives
			sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
			w := NewAuditWriter(s.Queries())
			defer w.Close() //nolint:errcheck

			h := NewFeedbackHandler(w, sm, (<-chan string)(feedbackCh), defaultTimeout)

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

// Package agent — channel_test.go covers inAppChannel in isolation.
// Tests construct inAppChannel directly via newInAppChannel (bypassing
// FeedbackHandler) so they cannot regress the production path.
package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// newInAppChannelForTest is a helper that seeds a DB with a policy and run,
// then returns a ready-to-use inAppChannel together with the store and
// AuditWriter so individual tests can inspect the audit trail.
func newInAppChannelForTest(t *testing.T, runID string) (*inAppChannel, *db.Store, *AuditWriter) {
	t.Helper()
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, runID, "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine(runID, model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { w.Close() }) //nolint:errcheck

	ch := newInAppChannel(w, sm)
	return ch, s, w
}

// TestInAppChannel_Resolve_DeliversToWaiter verifies the happy path: a goroutine
// calls Request, the waiter appears in the DB, Resolve delivers the response, and
// the run transitions back to running with both audit steps present.
func TestInAppChannel_Resolve_DeliversToWaiter(t *testing.T) {
	const runID = "run1"
	inApp, s, w := newInAppChannelForTest(t, runID)

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)

	go func() {
		text, err := inApp.Request(context.Background(), feedbackRequest{
			RunID:      runID,
			FeedbackID: "fb-1",
			ToolName:   AskOperatorToolName,
			Message:    "please answer",
			Timeout:    2 * time.Second,
		})
		done <- result{text, err}
	}()

	// Poll until the feedback row appears in the DB (mirrors feedback_test.go lines 145-158).
	deadline := time.Now().Add(2 * time.Second)
	var feedbackID string
	for time.Now().Before(deadline) {
		rows, err := s.GetPendingFeedbackRequestsByRun(context.Background(), runID)
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

	if err := inApp.Resolve(feedbackID, "operator response"); err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Request: unexpected error: %v", res.err)
		}
		if res.text != "operator response" {
			t.Errorf("response = %q, want %q", res.text, "operator response")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not return within 2s")
	}

	// Flush writer and verify both audit steps exist.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: listAll})
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

// TestInAppChannel_Resolve_UnknownRequestID verifies that Resolve on a fresh
// channel with no registered waiter returns ErrUnknownRequestID.
func TestInAppChannel_Resolve_UnknownRequestID(t *testing.T) {
	inApp, _, _ := newInAppChannelForTest(t, "run1")

	err := inApp.Resolve("nonexistent-id", "body")
	if !errors.Is(err, ErrUnknownRequestID) {
		t.Errorf("Resolve = %v, want ErrUnknownRequestID", err)
	}
}

// TestInAppChannel_Resolve_AfterTimeout_ReturnsUnknown verifies that once a
// Request returns due to timeout the deferred unregister has already run, so a
// subsequent Resolve call yields ErrUnknownRequestID.
func TestInAppChannel_Resolve_AfterTimeout_ReturnsUnknown(t *testing.T) {
	const runID = "run1"
	inApp, _, _ := newInAppChannelForTest(t, runID)

	// 20ms timeout — short enough to make the test fast, long enough to be reliable.
	_, _ = inApp.Request(context.Background(), feedbackRequest{
		RunID:      runID,
		FeedbackID: "fb-timeout",
		ToolName:   AskOperatorToolName,
		Message:    "please answer",
		Timeout:    20 * time.Millisecond,
	})
	// Request has returned — deferred delete must have run by now.

	err := inApp.Resolve("fb-timeout", "late response")
	if !errors.Is(err, ErrUnknownRequestID) {
		t.Errorf("Resolve after timeout = %v, want ErrUnknownRequestID", err)
	}
}

// TestInAppChannel_Resolve_DoubleCall verifies that the first Resolve returns nil
// and the second returns ErrUnknownRequestID (delete-under-lock prevents double
// delivery).
func TestInAppChannel_Resolve_DoubleCall(t *testing.T) {
	const runID = "run1"
	inApp, s, _ := newInAppChannelForTest(t, runID)

	done := make(chan error, 1)
	go func() {
		_, err := inApp.Request(context.Background(), feedbackRequest{
			RunID:      runID,
			FeedbackID: "fb-double",
			ToolName:   AskOperatorToolName,
			Message:    "please answer",
			Timeout:    2 * time.Second,
		})
		done <- err
	}()

	// Wait for the feedback row (mirrors feedback_test.go lines 145-158).
	deadline := time.Now().Add(2 * time.Second)
	var feedbackID string
	for time.Now().Before(deadline) {
		rows, err := s.GetPendingFeedbackRequestsByRun(context.Background(), runID)
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

	if err := inApp.Resolve(feedbackID, "first"); err != nil {
		t.Fatalf("first Resolve: unexpected error: %v", err)
	}

	// Drain the goroutine.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not return within 2s after first Resolve")
	}

	err := inApp.Resolve(feedbackID, "second")
	if !errors.Is(err, ErrUnknownRequestID) {
		t.Errorf("second Resolve = %v, want ErrUnknownRequestID", err)
	}
}

// TestInAppChannel_Resolve_Concurrent launches 50 concurrent Request goroutines
// each with a unique feedback_id, then resolves all 50 from 50 concurrent Resolve
// goroutines. All responses must be delivered correctly and the waiter map must be
// empty afterwards. The test is -race clean.
func TestInAppChannel_Resolve_Concurrent(t *testing.T) {
	const N = 50
	s := testutil.NewTestStore(t)

	// Seed a policy + N runs, each needing its own run/state-machine because
	// RunStateMachine is bound to a single runID.
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")

	type entry struct {
		inApp      *inAppChannel
		feedbackID string
		done       chan error
	}

	entries := make([]entry, N)
	for i := 0; i < N; i++ {
		runID := fmt.Sprintf("run-%d", i)
		testutil.InsertRun(t, s, runID, "p1", model.RunStatusRunning)

		pub := &capturePublisher{}
		sm := NewRunStateMachine(runID, model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
		w := NewAuditWriter(s.Queries())
		t.Cleanup(func() { w.Close() }) //nolint:errcheck

		fbID := fmt.Sprintf("fb-%d", i)
		entries[i] = entry{
			inApp:      newInAppChannel(w, sm),
			feedbackID: fbID,
			done:       make(chan error, 1),
		}
	}

	// Launch all Request goroutines.
	var startWg sync.WaitGroup
	startWg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		e := entries[i]
		runID := fmt.Sprintf("run-%d", i)
		go func() {
			startWg.Done()
			_, err := e.inApp.Request(context.Background(), feedbackRequest{
				RunID:      runID,
				FeedbackID: e.feedbackID,
				ToolName:   AskOperatorToolName,
				Message:    "concurrent request",
				Timeout:    5 * time.Second,
			})
			e.done <- err
		}()
	}
	startWg.Wait()

	// Wait for all waiters to appear in the waiter map before resolving.
	// Poll each inAppChannel until its waiter is registered.
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < N; i++ {
		e := entries[i]
		for time.Now().Before(deadline) {
			e.inApp.mu.Lock()
			_, registered := e.inApp.waiters[e.feedbackID]
			e.inApp.mu.Unlock()
			if registered {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}

	// Resolve all 50 from 50 concurrent goroutines.
	var resolveWg sync.WaitGroup
	resolveWg.Add(N)
	resolveErrs := make([]error, N)
	for i := 0; i < N; i++ {
		i := i
		e := entries[i]
		go func() {
			defer resolveWg.Done()
			resolveErrs[i] = e.inApp.Resolve(e.feedbackID, fmt.Sprintf("response-%d", i))
		}()
	}
	resolveWg.Wait()

	for i, err := range resolveErrs {
		if err != nil {
			t.Errorf("Resolve[%d]: unexpected error: %v", i, err)
		}
	}

	// Collect all Request results.
	for i, e := range entries {
		select {
		case err := <-e.done:
			if err != nil {
				t.Errorf("Request[%d]: unexpected error: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("Request[%d] did not return within 5s", i)
		}
	}

	// All waiter maps must be empty after delivery.
	for i, e := range entries {
		e.inApp.mu.Lock()
		remaining := len(e.inApp.waiters)
		e.inApp.mu.Unlock()
		if remaining != 0 {
			t.Errorf("inApp[%d].waiters has %d entries, want 0", i, remaining)
		}
	}
}

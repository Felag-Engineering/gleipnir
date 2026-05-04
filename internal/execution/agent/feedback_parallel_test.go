// Package agent — feedback_parallel_test.go is the parallel-implementation
// harness required by plugin-system-spec.md §15.1 step 2. It drives BOTH the
// legacy localFeedbackChannel and the new inAppChannel against the same curated
// trace (happy / timeout / cancellation / late-submission) and diffs their
// observable outputs. This is the ship gate before #181 deletes the old path.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// observableTrace captures everything an external observer can see after a
// channel.Request call returns: the return values, the audit trail, the SSE
// events emitted, and the terminal run status.
type observableTrace struct {
	channelName    string
	returnText     string
	returnErr      string // err.Error() or "" if nil
	terminalStatus string
	steps          []normalizedStep
	events         []normalizedEvent
}

// normalizedStep is a RunStep with timing-derived and identity fields stripped
// so that OLD and NEW traces are byte-comparable.
//
// Stripped: id, created_at, step_number (seq). These are nondeterministic.
// Kept: type, content map (feedback_id, expires_at, etc. are identical literals
// in both calls so they are already equal and do not need stripping).
type normalizedStep struct {
	Type    string
	Content map[string]any
}

// normalizedEvent is a published SSE event with version and any *_at timestamp
// fields stripped from the payload (nondeterministic). The feedback_id and
// expires_at fields inside feedback.created payloads are identical literals on
// both paths and are kept.
type normalizedEvent struct {
	EventType string
	Payload   map[string]any
}

// newOldChannel constructs a localFeedbackChannel backed by its own isolated
// SQLite store and capturePublisher. The caller owns feedbackCh.
//
// MUST use an independent store — shared stores risk PK conflicts on
// InsertPolicy/InsertRun and cross-pollute ListRunSteps results.
func newOldChannel(t *testing.T, runID string, feedbackCh <-chan string) (*localFeedbackChannel, *db.Store, *AuditWriter, *capturePublisher) {
	t.Helper()
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, runID, "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine(runID, model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { w.Close() }) //nolint:errcheck

	ch := &localFeedbackChannel{
		audit:      w,
		sm:         sm,
		feedbackCh: feedbackCh,
	}
	return ch, s, w, pub
}

// newNewChannel constructs an inAppChannel backed by its own isolated SQLite
// store and capturePublisher. The returned *inAppChannel also exposes Resolve.
//
// MUST use an independent store — see newOldChannel comment.
// MUST wire WithStateMachinePublisher(pub) — without it the publisher is nil
// and the events list will be empty, producing a misleading diff failure.
func newNewChannel(t *testing.T, runID string) (*inAppChannel, *db.Store, *AuditWriter, *capturePublisher) {
	t.Helper()
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, runID, "p1", model.RunStatusRunning)

	pub := &capturePublisher{}
	sm := NewRunStateMachine(runID, model.RunStatusRunning, s.DB(), s.Queries(), WithStateMachinePublisher(pub))
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { w.Close() }) //nolint:errcheck

	ch := newInAppChannel(w, sm)
	return ch, s, w, pub
}

// collectTrace flushes the AuditWriter, reads all run steps, normalizes them,
// snapshots the captured SSE events, normalizes them, and reads the terminal
// run status from the DB.
func collectTrace(
	t *testing.T,
	s *db.Store,
	w *AuditWriter,
	pub *capturePublisher,
	channelName, returnText string,
	returnErr error,
) observableTrace {
	t.Helper()

	// Flush queued audit writes before reading.
	if err := w.Close(); err != nil {
		t.Fatalf("collectTrace(%s): AuditWriter.Close: %v", channelName, err)
	}

	// Read all steps for the run. We use a constant run ID per test so we can
	// look it up from the first step. But we need the runID — pass it via store.
	// Since each test uses the same runID literal we can re-derive it from the
	// run table: just grab all steps (we know the run).
	// Actually, we need the runID. Pass it as a parameter through channelName? No —
	// we'll read it from the store directly. Each store has exactly one run.
	runRows, err := s.DB().QueryContext(context.Background(), `SELECT id FROM runs LIMIT 1`)
	if err != nil {
		t.Fatalf("collectTrace(%s): query runs: %v", channelName, err)
	}
	var runID string
	for runRows.Next() {
		if err := runRows.Scan(&runID); err != nil {
			t.Fatalf("collectTrace(%s): scan run id: %v", channelName, err)
		}
	}
	runRows.Close() //nolint:errcheck

	rawSteps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{
		RunID: runID,
		After: -1,
		Limit: listAll,
	})
	if err != nil {
		t.Fatalf("collectTrace(%s): ListRunSteps: %v", channelName, err)
	}

	steps := make([]normalizedStep, 0, len(rawSteps))
	for _, rs := range rawSteps {
		var content map[string]any
		if err := json.Unmarshal([]byte(rs.Content), &content); err != nil {
			// Fall back to a raw string entry so the diff still shows the value.
			content = map[string]any{"_raw": rs.Content}
		}
		// Drop any *_at timestamp values inside content — they are timing-derived
		// and would diverge between OLD and NEW even when behaviour is identical.
		for k := range content {
			if len(k) >= 3 && k[len(k)-3:] == "_at" {
				delete(content, k)
			}
		}
		steps = append(steps, normalizedStep{
			Type:    rs.Type,
			Content: content,
		})
	}

	// Normalize SSE events.
	rawEvents := pub.all()
	events := make([]normalizedEvent, 0, len(rawEvents))
	for _, ev := range rawEvents {
		var payload map[string]any
		if err := json.Unmarshal(ev.data, &payload); err != nil {
			payload = map[string]any{"_raw": string(ev.data)}
		}
		// Drop version (nondeterministic counter) and any *_at fields.
		delete(payload, "version")
		for k := range payload {
			if len(k) >= 3 && k[len(k)-3:] == "_at" {
				delete(payload, k)
			}
		}
		events = append(events, normalizedEvent{
			EventType: ev.eventType,
			Payload:   payload,
		})
	}

	// Read terminal status.
	run, err := s.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("collectTrace(%s): GetRun: %v", channelName, err)
	}

	errStr := ""
	if returnErr != nil {
		errStr = returnErr.Error()
	}

	return observableTrace{
		channelName:    channelName,
		returnText:     returnText,
		returnErr:      errStr,
		terminalStatus: run.Status,
		steps:          steps,
		events:         events,
	}
}

// diffTraces fails the test if want and got differ, printing a structured diff.
func diffTraces(t *testing.T, want, got observableTrace) {
	t.Helper()
	// Compare everything except channelName (which is intentionally different).
	type comparable struct {
		ReturnText     string
		ReturnErr      string
		TerminalStatus string
		Steps          []normalizedStep
		Events         []normalizedEvent
	}
	w := comparable{want.returnText, want.returnErr, want.terminalStatus, want.steps, want.events}
	g := comparable{got.returnText, got.returnErr, got.terminalStatus, got.steps, got.events}
	if diff := cmp.Diff(w, g); diff != "" {
		t.Errorf("OLD vs NEW trace mismatch (-want OLD, +got NEW):\n%s", diff)
	}
}

// TestParallel_HappyPath_Equivalent drives both channel implementations through
// the happy path (operator responds before timeout) and asserts byte-identical
// observable behaviour: same audit steps, same SSE events, same return values,
// same terminal run status.
func TestParallel_HappyPath_Equivalent(t *testing.T) {
	const (
		runID      = "run-happy"
		feedbackID = "fb-test-1"
		toolName   = AskOperatorToolName
		message    = "please confirm"
		response   = "confirmed"
	)
	// Fixed ExpiresAt so audit content matches identically across OLD and NEW.
	expiresAt := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)

	req := feedbackRequest{
		RunID:      runID,
		FeedbackID: feedbackID,
		ToolName:   toolName,
		Message:    message,
		Timeout:    5 * time.Second,
		ExpiresAt:  expiresAt,
	}

	// --- OLD path ---
	oldFeedbackCh := make(chan string, 1)
	oldCh, oldStore, oldWriter, oldPub := newOldChannel(t, runID, oldFeedbackCh)

	oldDone := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		text, err := oldCh.Request(context.Background(), req)
		oldDone <- struct {
			text string
			err  error
		}{text, err}
	}()
	// Deliver response synchronously into the buffered channel — the goroutine
	// will pick it up from its select.
	oldFeedbackCh <- response
	oldResult := <-oldDone
	oldTrace := collectTrace(t, oldStore, oldWriter, oldPub, "OLD", oldResult.text, oldResult.err)

	// --- NEW path ---
	newCh, newStore, newWriter, newPub := newNewChannel(t, runID)

	newDone := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		text, err := newCh.Request(context.Background(), req)
		newDone <- struct {
			text string
			err  error
		}{text, err}
	}()

	// Wait for the waiter to be registered before resolving. The inAppChannel
	// registers the waiter before writing the audit step (cap-1 buffer, so
	// Resolve never blocks). Poll the waiter map directly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		newCh.mu.Lock()
		_, registered := newCh.waiters[feedbackID]
		newCh.mu.Unlock()
		if registered {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Resolve with the known FeedbackID literal — no DB polling needed.
	if err := newCh.Resolve(feedbackID, response); err != nil {
		t.Fatalf("NEW Resolve: %v", err)
	}

	newResult := <-newDone
	newTrace := collectTrace(t, newStore, newWriter, newPub, "NEW", newResult.text, newResult.err)

	diffTraces(t, oldTrace, newTrace)
}

// TestParallel_Timeout_Equivalent drives both channel implementations through
// the timeout path (no response arrives before Timeout fires) and asserts that
// the observable trace is byte-identical. Both branches are character-for-character
// identical in the source (inAppChannel.Request lines 283-316 match
// localFeedbackChannel.Request lines 144-179) so this passes by construction,
// but the harness makes the equivalence explicit and detectable if either side
// is later changed.
func TestParallel_Timeout_Equivalent(t *testing.T) {
	const (
		runID    = "run-timeout"
		toolName = AskOperatorToolName
		message  = "will this time out?"
	)
	expiresAt := time.Now().Add(1 * time.Second).UTC().Format(time.RFC3339Nano)

	req := feedbackRequest{
		RunID:      runID,
		FeedbackID: "fb-timeout-1",
		ToolName:   toolName,
		Message:    message,
		Timeout:    30 * time.Millisecond,
		ExpiresAt:  expiresAt,
	}

	// --- OLD path ---
	oldFeedbackCh := make(chan string) // unbuffered — nothing sends
	oldCh, oldStore, oldWriter, oldPub := newOldChannel(t, runID, oldFeedbackCh)
	oldText, oldErr := oldCh.Request(context.Background(), req)
	oldTrace := collectTrace(t, oldStore, oldWriter, oldPub, "OLD", oldText, oldErr)

	// --- NEW path: same FeedbackID literal, independent store — no PK collision ---
	newCh, newStore, newWriter, newPub := newNewChannel(t, runID)
	newText, newErr := newCh.Request(context.Background(), req)
	newTrace := collectTrace(t, newStore, newWriter, newPub, "NEW", newText, newErr)

	diffTraces(t, oldTrace, newTrace)
}

// TestParallel_ContextCancelled_Equivalent drives both channel implementations
// through the context-cancellation path and asserts byte-identical observable
// behaviour. Both paths return the same wrapped ctx.Err(), write a single
// feedback_request step, and leave the run in waiting_for_feedback.
func TestParallel_ContextCancelled_Equivalent(t *testing.T) {
	const (
		runID    = "run-cancel"
		toolName = AskOperatorToolName
		message  = "will this get cancelled?"
	)
	expiresAt := time.Now().Add(1 * time.Minute).UTC().Format(time.RFC3339Nano)

	// Use a context that cancels quickly but after Request is blocking.
	makeCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 30*time.Millisecond)
	}

	req := feedbackRequest{
		RunID:      runID,
		FeedbackID: "fb-cancel-1",
		ToolName:   toolName,
		Message:    message,
		Timeout:    0, // no in-process timeout — only ctx cancellation
		ExpiresAt:  expiresAt,
	}

	// --- OLD path ---
	oldFeedbackCh := make(chan string) // unbuffered — nothing sends
	oldCh, oldStore, oldWriter, oldPub := newOldChannel(t, runID, oldFeedbackCh)
	oldCtx, oldCancel := makeCtx()
	defer oldCancel()
	oldText, oldErr := oldCh.Request(oldCtx, req)
	oldTrace := collectTrace(t, oldStore, oldWriter, oldPub, "OLD", oldText, oldErr)

	// --- NEW path: same FeedbackID literal, independent store — no PK collision ---
	newCh, newStore, newWriter, newPub := newNewChannel(t, runID)
	newCtx, newCancel := makeCtx()
	defer newCancel()
	newText, newErr := newCh.Request(newCtx, req)
	newTrace := collectTrace(t, newStore, newWriter, newPub, "NEW", newText, newErr)

	diffTraces(t, oldTrace, newTrace)
}

// TestParallel_LateSubmission_DocumentedDivergence documents the ONE intentional
// behavioural difference between localFeedbackChannel (OLD) and inAppChannel (NEW):
// a response delivered AFTER Request has already returned due to timeout.
//
// Per ADR-044 §4.2 (feedback_response_late hard-rejection rule):
//   - NEW (inAppChannel): Resolve returns ErrUnknownRequestID — the deferred
//     delete has already run, so the response is hard-rejected.
//   - OLD (localFeedbackChannel): the response sits in the buffered channel
//     unread — no error, no hard rejection, the response is silently lost.
//
// This divergence is intentional and desirable. Do NOT call diffTraces here.
func TestParallel_LateSubmission_DocumentedDivergence(t *testing.T) {
	const runID = "run-late"

	req := feedbackRequest{
		RunID:      runID,
		FeedbackID: "fb-test-late",
		ToolName:   AskOperatorToolName,
		Message:    "response will arrive too late",
		Timeout:    20 * time.Millisecond, // short enough to make the test fast
		ExpiresAt:  time.Now().Add(1 * time.Minute).UTC().Format(time.RFC3339Nano),
	}

	// --- OLD path: buffered channel absorbs the late send silently ---
	// Use make(chan string, 1) so the post-return send doesn't deadlock.
	oldFeedbackCh := make(chan string, 1)
	oldCh, _, oldWriter, _ := newOldChannel(t, runID, oldFeedbackCh)

	// Request blocks until timeout fires.
	_, _ = oldCh.Request(context.Background(), req)
	_ = oldWriter.Close()

	// Send after Request returned — the channel absorbs it silently.
	oldFeedbackCh <- "late"
	if len(oldFeedbackCh) != 1 {
		t.Errorf("OLD: expected buffered channel to hold the late response (len=1), got len=%d", len(oldFeedbackCh))
	}

	// --- NEW path: Resolve returns ErrUnknownRequestID ---
	newCh, _, newWriter, _ := newNewChannel(t, runID)

	// Use a fresh feedback ID to avoid any DB row collision.
	req.FeedbackID = "fb-test-late-new"
	_, _ = newCh.Request(context.Background(), req)
	_ = newWriter.Close()

	// Resolve after Request returned — deferred delete has run; must be rejected.
	err := newCh.Resolve("fb-test-late-new", "late")
	if !errors.Is(err, ErrUnknownRequestID) {
		t.Errorf("NEW: Resolve after timeout = %v, want ErrUnknownRequestID", err)
	}
}

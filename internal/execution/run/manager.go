// Package run owns the run lifecycle: launching runs, tracking in-flight
// goroutines, and serving HTTP inspection and control endpoints.
package run

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	// ErrRunNotFound is returned when the run ID is not registered in the manager.
	ErrRunNotFound = errors.New("run not registered")
	// ErrNoReceiver is returned when the run is registered but no goroutine is
	// currently blocking on the relevant gate (TOCTOU: gate timed out or context
	// was cancelled between the caller's check and the channel send).
	ErrNoReceiver = errors.New("run is not waiting for this operation")
)

// FeedbackResolver is the per-run target that ResolveFeedback delegates to.
// *agent.FeedbackHandler satisfies it.
type FeedbackResolver interface {
	Resolve(requestID, body string) error
}

// trackedRun holds the per-run state that RunManager needs to cancel and
// communicate with an in-flight run goroutine.
type trackedRun struct {
	cancel context.CancelFunc
	// approval is the buffered (cap 1) channel that the BoundAgent's approval
	// gate reads from. Nilled by CancelAll to signal that no decision should
	// be delivered; nil means SendApproval returns ErrRunNotFound instead of
	// blocking forever on a nil channel send.
	approval chan bool
	// feedbackResolver is the inAppChannel-backed resolver for this run. Set
	// atomically with Register by RegisterWithFeedbackResolver. Nilled by
	// CancelAll so ResolveFeedback returns ErrRunNotFound rather than racing.
	feedbackResolver FeedbackResolver
	// waiters are closed by Deregister to unblock callers of WaitForDeregistration.
	waiters []chan struct{}
}

// pluginRunCanceller is the narrow interface RunManager uses to propagate run
// cancellation to the plugin dispatcher. internal/plugin/dispatch.Pool satisfies
// it structurally; the interface lives here so RunManager does NOT import that
// package (preserves the package boundary documented in CLAUDE.md).
type pluginRunCanceller interface {
	CancelRun(runID string)
}

// RunManager tracks active run goroutines so they can be cancelled on demand.
type RunManager struct {
	mu               sync.Mutex
	runs             map[string]*trackedRun
	wg               sync.WaitGroup
	pluginCanceller  pluginRunCanceller // nil = no plugin dispatcher wired (tests, pre-plugin runs)
}

// NewRunManager returns a RunManager with no plugin canceller wired.
// Pass the dispatcher via WithPluginCanceller if plugin tools are enabled.
func NewRunManager() *RunManager {
	return &RunManager{
		runs: make(map[string]*trackedRun),
	}
}

// WithPluginCanceller attaches a plugin dispatcher to the manager so that
// Cancel and CancelAll also drive CancelRun for every in-flight plugin call.
// Must be called before any runs are registered. nil is a no-op.
func (m *RunManager) WithPluginCanceller(c pluginRunCanceller) {
	m.pluginCanceller = c
}

// Register stores the cancel func and approval channel for the given run ID
// and increments the internal WaitGroup. Must be called before the run goroutine
// is launched. approvalCh must be a buffered (cap 1) channel that the BoundAgent's
// approval gate reads from; the cap-1 buffer is what makes the non-blocking select
// in SendApproval correct rather than lossy when the decision arrives before the
// agent blocks on the channel.
func (m *RunManager) Register(runID string, cancel context.CancelFunc, approvalCh chan bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wg.Add(1)
	m.runs[runID] = &trackedRun{
		cancel:   cancel,
		approval: approvalCh,
	}
}

// RegisterWithFeedbackResolver performs Register and resolver attachment under a
// single mu.Lock so production callers never observe a window where the run is
// tracked without a resolver. Used by launcher.go — the only production caller
// that must avoid the register/resolver-attach race window.
func (m *RunManager) RegisterWithFeedbackResolver(
	runID string,
	cancel context.CancelFunc,
	approvalCh chan bool,
	resolver FeedbackResolver,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wg.Add(1)
	m.runs[runID] = &trackedRun{
		cancel:           cancel,
		approval:         approvalCh,
		feedbackResolver: resolver,
	}
}

// RegisterFeedbackResolver attaches a FeedbackResolver to an already-registered
// run. Kept for tests and future late-attach scenarios; production uses
// RegisterWithFeedbackResolver. No-op if the run is not currently registered.
func (m *RunManager) RegisterFeedbackResolver(runID string, r FeedbackResolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tr, ok := m.runs[runID]; ok {
		tr.feedbackResolver = r
	}
}

// ResolveFeedback delivers an operator response to the run's FeedbackResolver.
// Lock discipline: resolver is copied under the lock, then called after unlock
// to avoid holding the lock during potentially blocking work and to prevent a
// TOCTOU race with CancelAll (which nils feedbackResolver under the same lock).
func (m *RunManager) ResolveFeedback(runID, requestID, body string) error {
	m.mu.Lock()
	tr, ok := m.runs[runID]
	if !ok || tr.feedbackResolver == nil {
		m.mu.Unlock()
		return ErrRunNotFound
	}
	resolver := tr.feedbackResolver // copy under lock
	m.mu.Unlock()                   // release BEFORE calling Resolve
	return resolver.Resolve(requestID, body)
}

// Cancel calls the cancel func for the given run ID. Returns ErrRunNotFound if
// the run ID is not registered or has already been cancelled (including by
// CancelAll). Does NOT call wg.Done — the goroutine's deferred Deregister is
// responsible for that.
//
// After the context cancel fires, CancelRun is called on the plugin canceller
// (if wired) outside the lock — the canceller acquires its own mutex and may
// block briefly on conn.Close in the worst case.
func (m *RunManager) Cancel(runID string) error {
	m.mu.Lock()
	tr, ok := m.runs[runID]
	// tr.cancel is nilled by CancelAll so that Cancel returns ErrRunNotFound
	// rather than panicking on a nil function call. The trackedRun entry itself
	// remains in the map until Deregister so the WaitGroup can be decremented.
	if !ok || tr.cancel == nil {
		m.mu.Unlock()
		return ErrRunNotFound
	}
	tr.cancel()
	tr.cancel = nil
	m.mu.Unlock()

	// Notify the plugin dispatcher about the cancellation outside the lock.
	// The dispatcher acquires its own mutex; holding m.mu here would risk a
	// lock-order violation and unnecessary latency on the conn.Close fallback.
	if m.pluginCanceller != nil {
		m.pluginCanceller.CancelRun(runID)
	}
	return nil
}

// Deregister removes the entry for the given run ID and signals the WaitGroup.
// Called when a run goroutine exits (normally or after cancellation). No-op if
// the run was never registered or has already been deregistered.
func (m *RunManager) Deregister(runID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tr, ok := m.runs[runID]
	if !ok {
		return
	}
	// Call the cancel func if it is still set (i.e. Cancel/CancelAll has not
	// already called it). This is the normal-exit path.
	if tr.cancel != nil {
		tr.cancel()
	}
	// Notify any callers blocked in WaitForDeregistration.
	for _, ch := range tr.waiters {
		close(ch)
	}
	delete(m.runs, runID)
	m.wg.Done()
}

// WaitForDeregistration blocks until the given run's goroutine calls Deregister
// or the timeout elapses. Returns true if the run deregistered within the
// timeout, false if the timeout expired first.
//
// If the run is not currently active (already deregistered or never registered),
// it returns true immediately.
func (m *RunManager) WaitForDeregistration(runID string, timeout time.Duration) bool {
	m.mu.Lock()
	tr, ok := m.runs[runID]
	if !ok {
		m.mu.Unlock()
		return true
	}
	// Register a waiter channel before releasing the lock so Deregister cannot
	// slip in between the lock release and the select below.
	ch := make(chan struct{})
	tr.waiters = append(tr.waiters, ch)
	// Unlock before blocking: the goroutine calling Deregister also acquires mu,
	// so holding it here would deadlock.
	m.mu.Unlock()

	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// sendToChannel sends value to ch without blocking. Returns ErrRunNotFound if
// ch is nil (nilled by CancelAll), or ErrNoReceiver if the buffer is full
// (TOCTOU: gate timed out or context was cancelled between the caller's status
// check and this send).
func sendToChannel[T any](ch chan T, value T) error {
	if ch == nil {
		return ErrRunNotFound
	}
	select {
	case ch <- value:
		return nil
	default:
		return ErrNoReceiver
	}
}

// SendApproval routes an approval decision to the run's waiting agent goroutine.
// Returns ErrRunNotFound if the run is not registered or CancelAll has already
// run, or ErrNoReceiver if the channel buffer is full (TOCTOU: approval gate
// timed out or context was cancelled between the caller's status check and this
// call).
func (m *RunManager) SendApproval(runID string, approved bool) error {
	m.mu.Lock()
	tr, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok {
		return ErrRunNotFound
	}
	return sendToChannel(tr.approval, approved)
}

// CancelAll cancels every in-flight run. It does NOT call wg.Done — each
// goroutine's deferred Deregister will do that when it exits.
//
// After all context cancels fire, CancelRun is called on the plugin canceller
// (if wired) for each run ID, outside the lock.
func (m *RunManager) CancelAll() {
	m.mu.Lock()
	// Snapshot run IDs before mutating so we can notify the canceller outside
	// the lock (the canceller acquires its own mutex).
	runIDs := make([]string, 0, len(m.runs))
	for id, tr := range m.runs {
		tr.cancel()
		runIDs = append(runIDs, id)
		// Nil out the fields that are no longer valid so that subsequent calls
		// to Cancel/SendApproval/ResolveFeedback return ErrRunNotFound rather than
		// panicking or blocking. The trackedRun entry itself stays in the map so
		// that Deregister can still call wg.Done when each goroutine exits.
		tr.cancel = nil
		tr.approval = nil
		tr.feedbackResolver = nil // niled so ResolveFeedback returns ErrRunNotFound
	}
	m.mu.Unlock()

	if m.pluginCanceller != nil {
		for _, id := range runIDs {
			m.pluginCanceller.CancelRun(id)
		}
	}
}

// Wait blocks until all registered goroutines have exited (i.e. called
// Deregister). Used during graceful shutdown to drain in-flight runs.
func (m *RunManager) Wait() {
	m.wg.Wait()
}

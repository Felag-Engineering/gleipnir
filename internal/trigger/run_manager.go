package trigger

import (
	"context"
	"sync"
	"time"
)

// RunManager tracks active run goroutines so they can be cancelled on demand.
type RunManager struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	// approvals maps run IDs to the approval channel for that run's current
	// approval gate. The channel is buffered (cap 1); SendApproval sends
	// non-blocking so it gracefully handles TOCTOU races (approval timed out,
	// context done). Cap 1 means a decision delivered before the agent reads
	// is held in the buffer rather than dropped.
	approvals map[string]chan bool
	// feedbacks maps run IDs to the feedback channel for that run's current
	// feedback gate. The channel is buffered (cap 1); SendFeedback sends
	// non-blocking so it gracefully handles TOCTOU races (context cancelled,
	// run ended). Cap 1 means a response delivered before the agent reads is
	// held in the buffer rather than dropped.
	feedbacks map[string]chan string
	// active tracks runs that have been registered but whose goroutine has not
	// yet exited. This is separate from cancels because CancelAll removes from
	// cancels without signalling the WaitGroup — the goroutine's deferred
	// Deregister is the sole owner of wg.Done().
	active map[string]bool
	// waiters maps run IDs to channels that are closed when the run deregisters.
	// Used by WaitForDeregistration to avoid busy-polling.
	waiters map[string][]chan struct{}
	wg      sync.WaitGroup
}

func NewRunManager() *RunManager {
	return &RunManager{
		cancels:   make(map[string]context.CancelFunc),
		approvals: make(map[string]chan bool),
		feedbacks: make(map[string]chan string),
		active:    make(map[string]bool),
		waiters:   make(map[string][]chan struct{}),
	}
}

// Register stores the cancel func, approval channel, and feedback channel for
// the given run ID and increments the internal WaitGroup. Must be called before
// the run goroutine is launched. approvalCh and feedbackCh must be buffered
// (cap 1) channels that the BoundAgent's gates read from; the cap-1 buffer is
// what makes the non-blocking select in SendApproval/SendFeedback correct
// rather than lossy when the decision arrives before the agent blocks on the
// channel.
func (m *RunManager) Register(runID string, cancel context.CancelFunc, approvalCh chan bool, feedbackCh chan string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wg.Add(1)
	m.cancels[runID] = cancel
	m.approvals[runID] = approvalCh
	m.feedbacks[runID] = feedbackCh
	m.active[runID] = true
}

// Cancel calls the cancel func for the given run ID and removes the entry.
// Returns false if the run ID is not found. Does NOT call wg.Done — the
// goroutine's deferred Deregister is responsible for that.
func (m *RunManager) Cancel(runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cancel, ok := m.cancels[runID]
	if !ok {
		return false
	}
	cancel()
	delete(m.cancels, runID)
	return true
}

// Deregister removes the entry for the given run ID and signals the WaitGroup.
// Called when a run goroutine exits (normally or after cancellation). No-op if
// the run was never registered or has already been deregistered.
func (m *RunManager) Deregister(runID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active[runID] {
		return
	}
	// Call the cancel func if it is still present (i.e. Cancel/CancelAll has
	// not already called it). This is the normal-exit path.
	if cancel, ok := m.cancels[runID]; ok {
		cancel()
		delete(m.cancels, runID)
	}
	delete(m.approvals, runID)
	delete(m.feedbacks, runID)
	delete(m.active, runID)
	// Notify any callers blocked in WaitForDeregistration.
	for _, ch := range m.waiters[runID] {
		close(ch)
	}
	delete(m.waiters, runID)
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
	if !m.active[runID] {
		m.mu.Unlock()
		return true
	}
	// Register a waiter channel before releasing the lock so Deregister cannot
	// slip in between the lock release and the select below.
	ch := make(chan struct{})
	m.waiters[runID] = append(m.waiters[runID], ch)
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

// SendApproval routes an approval decision to the run's waiting agent goroutine.
// Returns true if the decision was delivered, false if the run is not registered
// or no goroutine is currently blocking on the approval gate (TOCTOU window where
// the approval timed out or the context was cancelled between the caller's status
// check and this call).
func (m *RunManager) SendApproval(runID string, approved bool) bool {
	m.mu.Lock()
	ch, ok := m.approvals[runID]
	m.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- approved:
		return true
	default:
		return false
	}
}

// SendFeedback routes an operator's freeform response to the run's waiting agent
// goroutine. Returns true if the response was delivered, false if the run is not
// registered or no goroutine is currently blocking on the feedback gate (TOCTOU
// window where the context was cancelled between the caller's status check and
// this call).
func (m *RunManager) SendFeedback(runID string, response string) bool {
	m.mu.Lock()
	ch, ok := m.feedbacks[runID]
	m.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- response:
		return true
	default:
		return false
	}
}

// CancelAll cancels every in-flight run. It does NOT call wg.Done — each
// goroutine's deferred Deregister will do that when it exits.
func (m *RunManager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, cancel := range m.cancels {
		cancel()
		delete(m.cancels, id)
	}
	for id := range m.approvals {
		delete(m.approvals, id)
	}
	for id := range m.feedbacks {
		delete(m.feedbacks, id)
	}
}

// Wait blocks until all registered goroutines have exited (i.e. called
// Deregister). Used during graceful shutdown to drain in-flight runs.
func (m *RunManager) Wait() {
	m.wg.Wait()
}

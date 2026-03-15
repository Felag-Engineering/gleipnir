package trigger

import (
	"context"
	"sync"
)

// RunManager tracks active run goroutines so they can be cancelled on demand.
type RunManager struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	// active tracks runs that have been registered but whose goroutine has not
	// yet exited. This is separate from cancels because CancelAll removes from
	// cancels without signalling the WaitGroup — the goroutine's deferred
	// Deregister is the sole owner of wg.Done().
	active map[string]bool
	wg     sync.WaitGroup
}

func NewRunManager() *RunManager {
	return &RunManager{
		cancels: make(map[string]context.CancelFunc),
		active:  make(map[string]bool),
	}
}

// Register stores the cancel func for the given run ID and increments the
// internal WaitGroup. Must be called before the run goroutine is launched.
func (m *RunManager) Register(runID string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wg.Add(1)
	m.cancels[runID] = cancel
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
	delete(m.active, runID)
	m.wg.Done()
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
}

// Wait blocks until all registered goroutines have exited (i.e. called
// Deregister). Used during graceful shutdown to drain in-flight runs.
func (m *RunManager) Wait() {
	m.wg.Wait()
}

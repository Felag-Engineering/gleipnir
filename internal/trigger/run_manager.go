package trigger

import (
	"context"
	"sync"
)

// RunManager tracks active run goroutines so they can be cancelled on demand.
type RunManager struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	wg      sync.WaitGroup
}

func NewRunManager() *RunManager {
	return &RunManager{
		cancels: make(map[string]context.CancelFunc),
	}
}

// Register stores the cancel func for the given run ID and increments the
// internal WaitGroup. Must be called before the run goroutine is launched.
func (m *RunManager) Register(runID string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wg.Add(1)
	m.cancels[runID] = cancel
}

// Cancel calls the cancel func for the given run ID and removes the entry.
// Returns false if the run ID is not found.
func (m *RunManager) Cancel(runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cancel, ok := m.cancels[runID]
	if !ok {
		return false
	}
	cancel()
	delete(m.cancels, runID)
	m.wg.Done()
	return true
}

// Deregister removes the entry for the given run ID and signals the WaitGroup.
// Called when a run terminates normally. No-op if already removed.
func (m *RunManager) Deregister(runID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.cancels[runID]; !ok {
		return
	}
	delete(m.cancels, runID)
	m.wg.Done()
}

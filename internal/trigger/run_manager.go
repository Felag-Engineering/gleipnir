package trigger

import (
	"context"
	"sync"
)

// RunManager tracks active run goroutines so they can be cancelled on demand.
type RunManager struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func NewRunManager() *RunManager {
	return &RunManager{
		cancels: make(map[string]context.CancelFunc),
	}
}

// Register stores the cancel func for the given run ID.
// Must be called before the run goroutine is launched.
func (m *RunManager) Register(runID string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	return true
}

// Deregister removes the entry for the given run ID.
// Called when a run terminates normally. No-op if already removed.
func (m *RunManager) Deregister(runID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancels, runID)
}

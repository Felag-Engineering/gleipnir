package caphealth

import (
	"context"
	"slices"
	"sync"
)

// TargetSet is a TargetLister whose contents are replaced wholesale by whoever
// knows which instances are live (the reconciler's observe pass).
//
// Replacing the set touches nothing in the Registry: a self-reported fault on a
// still-live instance survives, and clearing it on a restart stays the
// prober's own observation (Prober.observeRestart), which keys off the
// ContainerID carried by the next target it sees.
type TargetSet struct {
	mu      sync.RWMutex
	targets []Target
}

// SetTargets replaces the set. The slice is copied, so the caller may reuse it.
func (s *TargetSet) SetTargets(targets []Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = slices.Clone(targets)
}

// ProbeTargets returns a copy of the current set.
func (s *TargetSet) ProbeTargets(context.Context) ([]Target, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.targets), nil
}

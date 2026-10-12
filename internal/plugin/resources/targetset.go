package resources

import (
	"context"
	"slices"
	"sync"
)

// TargetSet is a TargetLister whose contents are replaced wholesale by whoever
// knows which containers are live (the reconciler's observe pass).
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

// StatsTargets returns a copy of the current set.
func (s *TargetSet) StatsTargets(context.Context) ([]Target, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.targets), nil
}

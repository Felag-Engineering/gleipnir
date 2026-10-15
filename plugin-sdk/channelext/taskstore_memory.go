package channelext

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// MemoryTaskStore is an in-memory TaskStore for tests and as the default
// when a plugin has no volume. It loses every task on restart, so it is not
// suitable where a wait must survive one; use NewFileTaskStore for that.
type MemoryTaskStore struct {
	mu    sync.Mutex
	tasks map[string]Task
}

// NewMemoryTaskStore returns an empty MemoryTaskStore.
func NewMemoryTaskStore() *MemoryTaskStore {
	return &MemoryTaskStore{tasks: make(map[string]Task)}
}

func (s *MemoryTaskStore) Create(_ context.Context, n NewTask) (Task, error) {
	id, err := newTaskID()
	if err != nil {
		return Task{}, err
	}
	t := Task{ID: id, Status: TaskWorking, CreatedAt: n.CreatedAt, TTL: n.TTL}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[id] = t
	return t, nil
}

func (s *MemoryTaskStore) Get(_ context.Context, id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("task %q: %w", id, ErrTaskNotFound)
	}
	return t, nil
}

func (s *MemoryTaskStore) Complete(_ context.Context, id string, r Resolution) (Task, error) {
	return s.transition(id, func(t Task) (Task, error) { return completed(t, r) })
}

func (s *MemoryTaskStore) Cancel(_ context.Context, id string) (Task, error) {
	return s.transition(id, func(t Task) (Task, error) { return terminated(t, TaskCancelled, "") })
}

func (s *MemoryTaskStore) Fail(_ context.Context, id, message string) (Task, error) {
	return s.transition(id, func(t Task) (Task, error) { return terminated(t, TaskFailed, message) })
}

// transition is the read-check-write under one lock hold, which is what
// makes the terminal write a compare-and-swap.
func (s *MemoryTaskStore) transition(id string, apply func(Task) (Task, error)) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("task %q: %w", id, ErrTaskNotFound)
	}
	updated, err := apply(t)
	if err != nil && !errors.Is(err, ErrTaskExpired) {
		return updated, err
	}
	s.tasks[id] = updated
	return updated, err
}

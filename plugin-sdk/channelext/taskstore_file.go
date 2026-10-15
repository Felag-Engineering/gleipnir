package channelext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const taskFileExt = ".json"

// FileTaskStore is a durable TaskStore: one JSON file per task under a
// directory on the plugin's instance volume (/data), so an open wait survives
// a container restart.
//
// Every write goes to a temp file, is fsynced, renamed over the task's file,
// and the directory is fsynced. A task file therefore always holds either the
// previous state or the new one, never a torn mix, and a transition that
// returned nil is on disk.
//
// The compare-and-swap is a single mutex around read-check-write. That is
// sufficient only because one process owns the directory: the plugin
// container is the sole writer to its volume. Two processes sharing a
// directory would need file locking this type does not do.
type FileTaskStore struct {
	mu  sync.Mutex
	dir string
}

// NewFileTaskStore opens (creating if needed) a task store rooted at dir.
func NewFileTaskStore(dir string) (*FileTaskStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("channelext: task store: create %s: %w", dir, err)
	}
	return &FileTaskStore{dir: dir}, nil
}

// path maps a task ID to its file. The ID is checked against the shape
// newTaskID produces so a hostile taskId from tasks/get (e.g. "../x") can
// never name a file outside dir.
func (s *FileTaskStore) path(id string) (string, error) {
	if !taskIDPattern.MatchString(id) {
		return "", fmt.Errorf("task id %q is malformed: %w", id, ErrTaskNotFound)
	}
	return filepath.Join(s.dir, id+taskFileExt), nil
}

func (s *FileTaskStore) Create(_ context.Context, n NewTask) (Task, error) {
	id, err := newTaskID()
	if err != nil {
		return Task{}, err
	}
	t := Task{ID: id, Status: TaskWorking, CreatedAt: n.CreatedAt, TTL: n.TTL}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeLocked(t); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *FileTaskStore) Get(_ context.Context, id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked(id)
}

func (s *FileTaskStore) Complete(_ context.Context, id string, r Resolution) (Task, error) {
	return s.transition(id, func(t Task) (Task, error) { return completed(t, r) })
}

func (s *FileTaskStore) Cancel(_ context.Context, id string) (Task, error) {
	return s.transition(id, func(t Task) (Task, error) { return terminated(t, TaskCancelled, "") })
}

func (s *FileTaskStore) Fail(_ context.Context, id, message string) (Task, error) {
	return s.transition(id, func(t Task) (Task, error) { return terminated(t, TaskFailed, message) })
}

func (s *FileTaskStore) transition(id string, apply func(Task) (Task, error)) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.readLocked(id)
	if err != nil {
		return Task{}, err
	}
	updated, err := apply(t)
	if err != nil && !errors.Is(err, ErrTaskExpired) {
		return updated, err
	}
	if writeErr := s.writeLocked(updated); writeErr != nil {
		return Task{}, writeErr
	}
	return updated, err
}

// readLocked loads one task. Caller must hold s.mu.
func (s *FileTaskStore) readLocked(id string) (Task, error) {
	path, err := s.path(id)
	if err != nil {
		return Task{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Task{}, fmt.Errorf("task %q: %w", id, ErrTaskNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("channelext: task store: read task %q: %w", id, err)
	}
	var t Task
	if err := json.Unmarshal(raw, &t); err != nil {
		return Task{}, fmt.Errorf("channelext: task store: decode task %q: %w", id, err)
	}
	return t, nil
}

// writeLocked durably replaces a task's file. Caller must hold s.mu.
func (s *FileTaskStore) writeLocked(t Task) (err error) {
	path, err := s.path(t.ID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("channelext: task store: encode task %q: %w", t.ID, err)
	}

	tmp, err := os.CreateTemp(s.dir, ".task-*.tmp")
	if err != nil {
		return fmt.Errorf("channelext: task store: create temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err = tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("channelext: task store: write task %q: %w", t.ID, err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("channelext: task store: fsync task %q: %w", t.ID, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("channelext: task store: close task %q: %w", t.ID, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("channelext: task store: rename task %q: %w", t.ID, err)
	}
	// The rename is only durable once the directory entry is.
	if err = syncDir(s.dir); err != nil {
		return fmt.Errorf("channelext: task store: fsync directory: %w", err)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

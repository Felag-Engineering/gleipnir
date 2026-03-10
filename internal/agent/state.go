package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// RunStateMachine tracks and persists the status of a single agent run.
// It enforces the legal transition graph and writes every transition to the DB.
// Safe for concurrent use.
type RunStateMachine struct {
	runID     string
	current   model.RunStatus
	mu        sync.Mutex
	queries   *db.Queries
	publisher Publisher
}

// StateMachineOption configures a RunStateMachine at construction time.
type StateMachineOption func(*RunStateMachine)

// WithStateMachinePublisher injects a Publisher that receives run.status_changed
// events after each successful transition.
func WithStateMachinePublisher(p Publisher) StateMachineOption {
	return func(sm *RunStateMachine) {
		sm.publisher = p
	}
}

// NewRunStateMachine returns a RunStateMachine initialised to the given status.
// The initial status is not written to the DB — the caller is responsible for
// ensuring the DB row already reflects that state.
func NewRunStateMachine(runID string, initial model.RunStatus, queries *db.Queries, opts ...StateMachineOption) *RunStateMachine {
	sm := &RunStateMachine{
		runID:   runID,
		current: initial,
		queries: queries,
	}
	for _, opt := range opts {
		opt(sm)
	}
	return sm
}

// Transition validates and persists a run status transition.
// For failed and interrupted transitions, errMsg is written to the runs.error column.
// It is safe for concurrent calls from multiple goroutines.
func (sm *RunStateMachine) Transition(ctx context.Context, next model.RunStatus, errMsg string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !isLegalTransition(sm.current, next) {
		return fmt.Errorf("illegal run status transition: %s → %s", sm.current, next)
	}

	var completedAt *string
	if isTerminalStatus(next) {
		t := time.Now().UTC().Format(time.RFC3339Nano)
		completedAt = &t
	}

	if next == model.RunStatusFailed || next == model.RunStatusInterrupted {
		if err := sm.queries.UpdateRunError(ctx, db.UpdateRunErrorParams{
			Status:      string(next),
			Error:       &errMsg,
			CompletedAt: completedAt,
			ID:          sm.runID,
		}); err != nil {
			return fmt.Errorf("persisting run status %s: %w", next, err)
		}
	} else {
		if err := sm.queries.UpdateRunStatus(ctx, db.UpdateRunStatusParams{
			Status:      string(next),
			CompletedAt: completedAt,
			ID:          sm.runID,
		}); err != nil {
			return fmt.Errorf("persisting run status %s: %w", next, err)
		}
	}

	sm.current = next

	if sm.publisher != nil {
		data, err := json.Marshal(map[string]string{"run_id": sm.runID, "status": string(next)})
		if err != nil {
			return fmt.Errorf("marshal publish payload: %w", err)
		}
		sm.publisher.Publish("run.status_changed", data)
	}

	return nil
}

// Current returns the current run status. Safe for concurrent use.
func (sm *RunStateMachine) Current() model.RunStatus {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.current
}

// isLegalTransition reports whether transitioning from → to is permitted by the
// run state machine graph (see model.RunStatus for the full transition diagram).
func isLegalTransition(from, to model.RunStatus) bool {
	legal := [][2]model.RunStatus{
		{model.RunStatusPending, model.RunStatusRunning},
		{model.RunStatusPending, model.RunStatusFailed}, // DB write failure before the run starts
		{model.RunStatusRunning, model.RunStatusComplete},
		{model.RunStatusRunning, model.RunStatusFailed},
		{model.RunStatusRunning, model.RunStatusWaitingForApproval},
		{model.RunStatusRunning, model.RunStatusInterrupted},
		{model.RunStatusWaitingForApproval, model.RunStatusRunning},
		{model.RunStatusWaitingForApproval, model.RunStatusFailed},
		{model.RunStatusWaitingForApproval, model.RunStatusInterrupted},
	}
	for _, pair := range legal {
		if pair[0] == from && pair[1] == to {
			return true
		}
	}
	return false
}

// isTerminalStatus reports whether the given status is a terminal state (no
// further transitions are possible after reaching it).
func isTerminalStatus(s model.RunStatus) bool {
	return s == model.RunStatusComplete || s == model.RunStatusFailed || s == model.RunStatusInterrupted
}

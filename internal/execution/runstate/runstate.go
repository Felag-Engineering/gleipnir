// Package runstate provides package-level run status transition functions for
// use by components that do not have a live RunStateMachine instance (e.g.
// timeout scanners, orphan handlers). It also exports the canonical transition
// table so there is a single source of truth for legal state transitions.
package runstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// ErrIllegalTransition is returned when a requested status transition is not
// permitted by the run state machine graph.
var ErrIllegalTransition = errors.New("illegal run status transition")

// ErrTransitionConflict is returned when a CAS update finds that another writer
// already advanced the run's version before this caller could commit its own
// transition. The run is in a valid state in the DB — the caller must not treat
// this as a fatal error, only as a signal that its write was lost.
var ErrTransitionConflict = errors.New("run status transition lost to concurrent writer")

// legalTransitions is the run state machine graph. Each key is a non-terminal
// status; its value lists every status it may transition to. Terminal statuses
// (complete, failed, interrupted) have no entry — lookup returns nil, causing
// IsLegalTransition to return false for any transition out of a terminal state.
var legalTransitions = map[model.RunStatus][]model.RunStatus{
	model.RunStatusPending: {
		model.RunStatusRunning,
		model.RunStatusFailed, // DB write failure before the run starts
	},
	model.RunStatusRunning: {
		model.RunStatusComplete,
		model.RunStatusFailed,
		model.RunStatusWaitingForApproval,
		model.RunStatusWaitingForFeedback,
		model.RunStatusInterrupted,
	},
	model.RunStatusWaitingForApproval: {
		model.RunStatusRunning,
		model.RunStatusFailed,
		model.RunStatusInterrupted,
	},
	model.RunStatusWaitingForFeedback: {
		model.RunStatusRunning,
		model.RunStatusFailed,
		model.RunStatusInterrupted,
	},
}

// IsLegalTransition reports whether transitioning from → to is permitted by the
// run state machine graph (see model.RunStatus for the full transition diagram).
func IsLegalTransition(from, to model.RunStatus) bool {
	for _, allowed := range legalTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// TransitionRunFailed transitions a run to the "failed" status from any legal
// source state. It reads the current status and version from the DB, validates
// the transition, and applies a CAS UPDATE (WHERE id = ? AND version = ?) to
// detect concurrent writers. Returns ErrTransitionConflict when rows_affected == 0
// (another writer already advanced the row). Returns ErrIllegalTransition when the
// current status does not allow a transition to "failed".
//
// This function does NOT insert error steps — callers are responsible for
// creating domain-specific error steps before calling this function.
func TransitionRunFailed(ctx context.Context, queries *db.Queries, publisher event.Publisher, runID string, reason string) error {
	run, err := queries.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	if !IsLegalTransition(model.RunStatus(run.Status), model.RunStatusFailed) {
		return fmt.Errorf("%w: %s → failed", ErrIllegalTransition, run.Status)
	}

	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := queries.UpdateRunError(ctx, db.UpdateRunErrorParams{
		Status:          string(model.RunStatusFailed),
		Error:           &reason,
		CompletedAt:     &completedAt,
		ID:              runID,
		ExpectedVersion: run.Version,
	})
	if err != nil {
		return fmt.Errorf("persisting run status failed: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: run %s", ErrTransitionConflict, runID)
	}

	RecordTransition(model.RunStatus(run.Status), model.RunStatusFailed)

	logctx.Logger(ctx).InfoContext(ctx, "run status transition", "run_id", runID, "from", run.Status, "to", "failed")

	if publisher != nil {
		if data, err := json.Marshal(map[string]string{"run_id": runID, "status": "failed"}); err == nil {
			publisher.Publish("run.status_changed", data)
		}
	}

	return nil
}

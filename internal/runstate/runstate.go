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
	"log/slog"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/event"
	"github.com/rapp992/gleipnir/internal/model"
)

// ErrIllegalTransition is returned when a requested status transition is not
// permitted by the run state machine graph.
var ErrIllegalTransition = errors.New("illegal run status transition")

// IsLegalTransition reports whether transitioning from → to is permitted by the
// run state machine graph (see model.RunStatus for the full transition diagram).
func IsLegalTransition(from, to model.RunStatus) bool {
	legal := [][2]model.RunStatus{
		{model.RunStatusPending, model.RunStatusRunning},
		{model.RunStatusPending, model.RunStatusFailed}, // DB write failure before the run starts
		{model.RunStatusRunning, model.RunStatusComplete},
		{model.RunStatusRunning, model.RunStatusFailed},
		{model.RunStatusRunning, model.RunStatusWaitingForApproval},
		{model.RunStatusRunning, model.RunStatusWaitingForFeedback},
		{model.RunStatusRunning, model.RunStatusInterrupted},
		{model.RunStatusWaitingForApproval, model.RunStatusRunning},
		{model.RunStatusWaitingForApproval, model.RunStatusFailed},
		{model.RunStatusWaitingForApproval, model.RunStatusInterrupted},
		{model.RunStatusWaitingForFeedback, model.RunStatusRunning},
		{model.RunStatusWaitingForFeedback, model.RunStatusFailed},
		{model.RunStatusWaitingForFeedback, model.RunStatusInterrupted},
	}
	for _, pair := range legal {
		if pair[0] == from && pair[1] == to {
			return true
		}
	}
	return false
}

// TransitionRunFailed transitions a run to the "failed" status from any legal
// source state. It reads the current status from the DB, validates the
// transition, persists the failure, and publishes a run.status_changed event.
//
// This function does NOT insert error steps — callers are responsible for
// creating domain-specific error steps before calling this function.
//
// TOCTOU caveat: there is an inherent race between the GetRun read and the
// UpdateRunError write. Callers should use a conditional DB update
// (rows-affected guard) upstream of this call to prevent duplicate transitions
// under concurrency. The IsLegalTransition check here is defense-in-depth,
// not the primary concurrency gate.
func TransitionRunFailed(ctx context.Context, queries *db.Queries, publisher event.Publisher, runID string, reason string) error {
	run, err := queries.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	if !IsLegalTransition(model.RunStatus(run.Status), model.RunStatusFailed) {
		return fmt.Errorf("%w: %s → failed", ErrIllegalTransition, run.Status)
	}

	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := queries.UpdateRunError(ctx, db.UpdateRunErrorParams{
		Status:      string(model.RunStatusFailed),
		Error:       &reason,
		CompletedAt: &completedAt,
		ID:          runID,
	}); err != nil {
		return fmt.Errorf("persisting run status failed: %w", err)
	}

	slog.InfoContext(ctx, "run status transition", "run_id", runID, "from", run.Status, "to", "failed")

	if publisher != nil {
		if data, err := json.Marshal(map[string]string{"run_id": runID, "status": "failed"}); err == nil {
			publisher.Publish("run.status_changed", data)
		}
	}

	return nil
}

// Package agent — this file holds free-function error helpers shared by BoundAgent,
// ApprovalHandler, and FeedbackHandler. They are free functions (not methods) so the
// handlers can call them without holding a BoundAgent reference.
package agent

import (
	"context"
	"errors"

	"github.com/rapp992/gleipnir/internal/logctx"
	"github.com/rapp992/gleipnir/internal/model"
)

// failRun transitions the run to failed status and returns the original error.
// If the context is already cancelled, a background context is used so the DB
// write still lands.
//
// A ErrTransitionConflict from Transition is logged at info level (not error) because
// it means another writer already committed a terminal status — the run is in a
// consistent state, so logging at error would be misleading.
func failRun(ctx context.Context, sm *RunStateMachine, runErr error) error {
	transCtx := ctx
	if ctx.Err() != nil {
		transCtx = context.Background()
	}
	if tErr := sm.Transition(transCtx, model.RunStatusFailed, runErr.Error()); tErr != nil {
		if errors.Is(tErr, ErrTransitionConflict) {
			logctx.Logger(transCtx).InfoContext(transCtx, "transition lost to concurrent writer; run already in terminal state",
				"run_err", runErr)
		} else {
			logctx.Logger(transCtx).ErrorContext(transCtx, "failed to persist run failure status",
				"run_err", runErr, "transition_err", tErr)
		}
	}
	return runErr
}

// logTransitionError wraps a failRun call that intentionally discards errors from
// state transitions that fail after the run itself has already failed. Logs via
// slog when the transition itself errors so the failure is not silently swallowed.
func logTransitionError(ctx context.Context, sm *RunStateMachine, runErr error) {
	if tErr := failRun(ctx, sm, runErr); tErr != nil && tErr != runErr {
		logctx.Logger(ctx).ErrorContext(ctx, "run transition failed", "err", tErr)
	}
}

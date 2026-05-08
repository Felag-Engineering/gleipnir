// Package dispatch routes agent tool and channel calls to plugin instances
// over gRPC.  This file owns the persisted lifecycle for plugin pending
// requests: the two-status table (pending → resolved | timed_out) that mirrors
// the approval and feedback request lifecycles in internal/execution/runstate.
//
// NOTE on terminology: there are two state machines involved in a Request flow.
//
//	Persisted lifecycle (this file): pending → resolved | timed_out.
//	  Transitions are CAS-guarded via WHERE status='pending' on the
//	  plugin_pending_requests row (ADR-038 spirit; no version column on this
//	  table — status-equality CAS matches the feedback_requests precedent).
//
//	In-memory pre-ack micro-state: issued → pre_acked.
//	  Lives only inside Dispatcher.Request during the 5s pre-ack window.
//	  Never persisted; a pre-ack failure writes a feedback_dispatch_error step
//	  and returns ErrPreAckFailed without inserting a plugin_pending_requests row.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// ErrIllegalTransition is returned when a caller attempts a status transition
// that is not in legalTransitions.
var ErrIllegalTransition = errors.New("channel request: illegal status transition")

// ErrTransitionConflict is returned when the CAS UPDATE finds rows_affected == 0,
// meaning another writer (the timeout scanner or a concurrent Resolve call) already
// advanced the row.  Callers must treat this as "the row is in a valid state —
// your write was simply lost" rather than a fatal error.
var ErrTransitionConflict = errors.New("channel request: transition lost to concurrent writer")

// ChannelRequestStatus is the type for the plugin_pending_requests.status column.
type ChannelRequestStatus string

const (
	StatusPending   ChannelRequestStatus = "pending"
	StatusResolved  ChannelRequestStatus = "resolved"
	StatusTimedOut  ChannelRequestStatus = "timed_out"
)

// legalTransitions enumerates valid status progressions for plugin pending
// requests.  Only pending is non-terminal; resolved and timed_out have no
// entry so any attempt to transition out of them returns ErrIllegalTransition.
var legalTransitions = map[ChannelRequestStatus][]ChannelRequestStatus{
	StatusPending: {StatusResolved, StatusTimedOut},
}

// isLegalTransition reports whether transitioning from → to is permitted.
func isLegalTransition(from, to ChannelRequestStatus) bool {
	for _, allowed := range legalTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// TransitionResolved marks a plugin_pending_request as resolved with the given
// response JSON.  Returns ErrTransitionConflict when rows_affected == 0 (the
// scanner already marked it timed_out).
func TransitionResolved(ctx context.Context, q *db.Queries, requestID, responseJSON string) error {
	return transition(ctx, q, requestID, StatusResolved, &responseJSON)
}

// TransitionTimedOut marks a plugin_pending_request as timed_out.  Returns
// ErrTransitionConflict when rows_affected == 0 (another writer already resolved
// or timed-out the row).
func TransitionTimedOut(ctx context.Context, q *db.Queries, requestID string) error {
	return transition(ctx, q, requestID, StatusTimedOut, nil)
}

// transition is the shared implementation for all persisted status changes.
func transition(ctx context.Context, q *db.Queries, requestID string, to ChannelRequestStatus, response *string) error {
	if !isLegalTransition(StatusPending, to) {
		return fmt.Errorf("%w: pending → %s", ErrIllegalTransition, to)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := q.UpdatePluginPendingRequestStatus(ctx, db.UpdatePluginPendingRequestStatusParams{
		Status:     string(to),
		Response:   response,
		ResolvedAt: &now,
		ID:         requestID,
	})
	if err != nil {
		return fmt.Errorf("update plugin pending request status: %w", err)
	}
	if rows == 0 {
		// WHERE status='pending' guard: another writer already resolved this row.
		return fmt.Errorf("%w: request %s", ErrTransitionConflict, requestID)
	}
	return nil
}

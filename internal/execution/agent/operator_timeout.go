// Package agent — operator_timeout.go owns the two-writer timeout race shared by
// the approval and feedback handlers (#505). Both handlers suspend a run waiting
// for an operator and, on the in-app path, race a background timeout scanner
// (internal/timeout) to claim the pending row. Before this helper that race —
// claim, branch on rows, write-or-skip the error step, return win-or-sentinel —
// was copy-pasted into approval.go, feedback.go, and a dead third copy in
// channel.go, and had already drifted (only one copy bounded the claim write).
package agent

import (
	"context"
	"errors"
	"time"

	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// timeoutClaim captures the per-domain inputs to the shared timeout race. The
// approval and feedback handlers differ only in the claim query, the error code,
// and the two human-readable messages; everything else (the bounded-context
// claim, the rows==1/rows==0 branch, the error-step write) is identical and lives
// in claimRequestTimeout.
type timeoutClaim struct {
	// name labels log lines ("approval" | "feedback").
	name string

	// runID and requestID identify the suspended run and its pending row.
	runID     string
	requestID string

	// claim runs the conditional UPDATE ... WHERE status='pending' and returns
	// the number of rows affected. The closure binds the domain-specific query
	// (UpdateApprovalRequestStatus / UpdateFeedbackRequestStatus) and timeout
	// status string. now is a UTC RFC3339Nano timestamp for the resolution time.
	claim func(ctx context.Context, now string) (int64, error)

	// errorCode is written into the error step when this caller wins the race.
	errorCode model.ErrorCode

	// wonMessage is both the returned error and the error-step message when this
	// caller wins (rows==1). lostMessage is the sentinel error returned when the
	// scanner already claimed the row (rows==0) — no error step is written, since
	// the scanner already wrote one.
	wonMessage  string
	lostMessage string
}

// claimRequestTimeout runs the two-writer timeout race against the timeout
// scanner. The conditional UPDATE inside c.claim is the arbitration primitive:
// exactly one writer sees rows==1 and owns the side effects.
//
//   - rows==1: this caller won. Write the error step and return wonMessage.
//   - rows==0: the scanner already claimed the row and wrote its own error step
//     and run transition. Skip the error step (avoid a duplicate) and return the
//     lostMessage sentinel so Run() still terminates the run.
//
// The claim write runs on a fresh background context bounded to 5s: a fresh
// context so the fire-and-forget write is not cancelled when the parent run
// context is already done, and bounded so a stalled DB cannot hang the write
// indefinitely. The error-step write uses the parent ctx (the timer fired, so
// ctx is still live) to preserve log correlation.
func claimRequestTimeout(ctx context.Context, audit *AuditWriter, c timeoutClaim) error {
	dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := c.claim(dbCtx, now)
	if err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "failed to update "+c.name+" status on timeout",
			c.name+"_id", c.requestID, "err", err)
	}

	if rows == 1 {
		logAuditError(ctx, audit, Step{
			RunID:   c.runID,
			Type:    model.StepTypeError,
			Content: model.ErrorStepContent{Message: c.wonMessage, Code: c.errorCode},
		})
		return errors.New(c.wonMessage)
	}

	// Scanner won the race: it already wrote the error step and transitioned the
	// run. Return a sentinel so Run() knows to stop, but avoid a duplicate step.
	logctx.Logger(ctx).DebugContext(ctx, c.name+" already resolved by scanner",
		c.name+"_id", c.requestID)
	return errors.New(c.lostMessage)
}

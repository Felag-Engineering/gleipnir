// Package agent — this file implements ApprovalHandler, which manages the
// approval-gating lifecycle for tools marked approval: required (ADR-008).
// It holds no BoundAgent reference so it can be constructed and tested independently.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/logctx"
	"github.com/rapp992/gleipnir/internal/model"
)

// ApprovalHandler manages the approval-gate lifecycle for a single run.
// It writes the approval_request audit step, transitions the run to
// waiting_for_approval, and blocks on the approval channel, a timeout, or
// context cancellation. It holds no BoundAgent reference.
type ApprovalHandler struct {
	audit      *AuditWriter
	sm         *RunStateMachine
	approvalCh <-chan bool // receive-only: handler never closes the channel
}

// NewApprovalHandler constructs an ApprovalHandler. approvalCh must be
// receive-only (compile-time guarantee the handler does not close it).
func NewApprovalHandler(audit *AuditWriter, sm *RunStateMachine, approvalCh <-chan bool) *ApprovalHandler {
	return &ApprovalHandler{
		audit:      audit,
		sm:         sm,
		approvalCh: approvalCh,
	}
}

// Wait suspends the run at an approval gate for the given tool entry.
// It writes the approval_request audit step, transitions the run to
// waiting_for_approval (which creates the DB record and publishes approval.created
// via the state machine), then blocks on the approval channel, a timeout, or
// context cancellation. Returns nil if approved, error otherwise.
func (h *ApprovalHandler) Wait(ctx context.Context, runID string, entry resolvedToolEntry, internalName string, input map[string]any) error {
	approvalID := model.NewULID()

	proposedInput, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshalling proposed input for approval request: %w", err)
	}

	var expiresAt string
	if entry.tool.Timeout > 0 {
		expiresAt = time.Now().UTC().Add(entry.tool.Timeout).Format(time.RFC3339Nano)
	} else {
		expiresAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	}

	if err := h.audit.Write(ctx, Step{
		RunID:   runID,
		Type:    model.StepTypeApprovalRequest,
		Content: map[string]any{"tool": internalName, "input": input},
	}); err != nil {
		return fmt.Errorf("writing approval request step: %w", err)
	}

	if err := h.sm.Transition(ctx, model.RunStatusWaitingForApproval, "", WithApprovalPayload(ApprovalPayload{
		ApprovalID:    approvalID,
		ToolName:      internalName,
		ProposedInput: string(proposedInput),
		ExpiresAt:     expiresAt,
	})); err != nil {
		return fmt.Errorf("transitioning run to waiting_for_approval: %w", err)
	}

	// nil timeoutCh (when Timeout == 0) blocks forever in the select,
	// meaning no timeout is applied. Use NewTimer so we can Stop it
	// on early approval — time.After leaks until the duration fires.
	var timeoutCh <-chan time.Time
	if entry.tool.Timeout > 0 {
		timer := time.NewTimer(entry.tool.Timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case approved := <-h.approvalCh:
		if !approved {
			err := fmt.Errorf("tool call %s rejected by operator", internalName)
			logAuditError(ctx, h.audit, Step{
				RunID:   runID,
				Type:    model.StepTypeError,
				Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeApprovalRejected},
			})
			return err
		}
		if err := h.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
			return fmt.Errorf("transitioning run back to running after approval: %w", err)
		}
	case <-timeoutCh:
		logctx.Logger(ctx).WarnContext(ctx, "approval timeout reached",
			"tool", internalName,
			"timeout", entry.tool.Timeout.String())
		now := time.Now().UTC().Format(time.RFC3339Nano)
		// Race the scanner: only the first writer (rows==1) owns the error step.
		// If the scanner already resolved it (rows==0), return a sentinel error so
		// Run() still terminates, but skip logAuditError to avoid a duplicate step.
		rows, dbErr := h.sm.Queries().UpdateApprovalRequestStatus(
			context.Background(),
			db.UpdateApprovalRequestStatusParams{
				Status:    string(model.ApprovalStatusTimeout),
				DecidedAt: &now,
				Note:      nil,
				ID:        approvalID,
			},
		)
		if dbErr != nil {
			logctx.Logger(ctx).WarnContext(ctx, "failed to update approval status on timeout", "approval_id", approvalID, "err", dbErr)
		}
		if rows == 1 {
			err := fmt.Errorf("approval timeout for tool %s", internalName)
			logAuditError(ctx, h.audit, Step{
				RunID:   runID,
				Type:    model.StepTypeError,
				Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeApprovalRejected},
			})
			return err
		}
		// Scanner won the race: it already wrote the error step and transitioned
		// the run. Return a sentinel so Run() knows to stop, but avoid a duplicate step.
		logctx.Logger(ctx).DebugContext(ctx, "approval already resolved by scanner", "approval_id", approvalID)
		return fmt.Errorf("approval timeout for tool %s: already resolved by scanner", internalName)
	case <-ctx.Done():
		return fmt.Errorf("context cancelled waiting for approval: %w", ctx.Err())
	}

	return nil
}

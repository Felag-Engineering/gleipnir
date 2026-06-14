// Package agent — this file implements ApprovalHandler, which manages the
// approval-gating lifecycle for tools marked approval: required (ADR-008).
// It holds no BoundAgent reference so it can be constructed and tested independently.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// ApprovalHandler manages the approval-gate lifecycle for a single run.
// It writes the approval_request audit step, transitions the run to
// waiting_for_approval, and blocks on the approval channel, a timeout, or
// context cancellation. It holds no BoundAgent reference.
type ApprovalHandler struct {
	audit      *AuditWriter
	sm         *RunStateMachine
	approvalCh <-chan bool // receive-only: handler never closes the channel

	// Plugin channel routing — populated by WithApprovalChannelDispatch.
	// When non-nil the handler dispatches through the plugin channel instead
	// of blocking on approvalCh.  Falls back to in-app on ErrApprovalRouteToInApp.
	channelDispatcher ApprovalChannelDispatcher
	policyID          string
	audienceID        string
}

// ApprovalHandlerOption is a functional option for NewApprovalHandler.
type ApprovalHandlerOption func(*ApprovalHandler)

// WithApprovalChannelDispatch attaches a plugin channel dispatcher to the
// handler.  When d is non-nil and audienceID is non-empty, Wait routes the
// approval through the plugin channel instead of blocking on approvalCh.
func WithApprovalChannelDispatch(d ApprovalChannelDispatcher, audienceID, policyID string) ApprovalHandlerOption {
	return func(h *ApprovalHandler) {
		h.channelDispatcher = d
		h.audienceID = audienceID
		h.policyID = policyID
	}
}

// NewApprovalHandler constructs an ApprovalHandler. approvalCh must be
// receive-only (compile-time guarantee the handler does not close it).
// Optional functional opts (e.g. WithApprovalChannelDispatch) are applied
// after initialization; existing callers that pass zero opts are unaffected.
func NewApprovalHandler(audit *AuditWriter, sm *RunStateMachine, approvalCh <-chan bool, opts ...ApprovalHandlerOption) *ApprovalHandler {
	h := &ApprovalHandler{
		audit:      audit,
		sm:         sm,
		approvalCh: approvalCh,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// resolveApprovalRecord updates the approval_requests DB row to the terminal
// status and emits the approval.resolved SSE event. Both operations are
// best-effort: the agent goroutine must resume regardless of DB or marshal
// errors. SSE is gated on the CAS winning (rows > 0) — if the scanner already
// timed out the row, the SSE must not be emitted for a stale decision.
func (h *ApprovalHandler) resolveApprovalRecord(ctx context.Context, runID, approvalID string, approved bool) {
	dbStatus := string(model.ApprovalStatusApproved)
	if !approved {
		dbStatus = string(model.ApprovalStatusRejected)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := h.sm.Queries().UpdateApprovalRequestStatus(ctx, db.UpdateApprovalRequestStatusParams{
		Status:    dbStatus,
		DecidedAt: &now,
		Note:      nil,
		ID:        approvalID,
	})
	if err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "plugin approval: UpdateApprovalRequestStatus failed",
			"approval_id", approvalID, "run_id", runID, "err", err)
		return
	}
	if rows == 0 {
		// Scanner already resolved this row (e.g. timeout beat the callback).
		logctx.Logger(ctx).DebugContext(ctx, "plugin approval: already resolved by scanner",
			"approval_id", approvalID, "run_id", runID)
		return
	}

	if pub := h.sm.Publisher(); pub != nil {
		payload := map[string]string{
			"approval_id": approvalID,
			"run_id":      runID,
			"status":      dbStatus,
		}
		if data, marshalErr := json.Marshal(payload); marshalErr == nil {
			pub.Publish("approval.resolved", data)
		}
	}
}

// formatApprovalPrompt builds a human-readable description of the pending
// approval gate.  The prompt is sent to the plugin channel (e.g. Slack) so the
// operator understands what they are approving without needing to open the UI.
func formatApprovalPrompt(toolName string, input map[string]any) string {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Sprintf("Approval required for tool `%s`", toolName)
	}
	return fmt.Sprintf("Approval required for tool `%s` with input: %s", toolName, string(inputJSON))
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

	// Plugin channel routing: when a dispatcher and audience are configured,
	// route the approval through the plugin (e.g. Slack approve/deny buttons).
	// ErrApprovalRouteToInApp falls through to the existing approvalCh select
	// below; all other results (approved, denied, error) are terminal.
	if h.channelDispatcher != nil && h.audienceID != "" {
		var expiresAtTime *time.Time
		if entry.tool.Timeout > 0 {
			t := time.Now().UTC().Add(entry.tool.Timeout)
			expiresAtTime = &t
		}
		prompt := formatApprovalPrompt(internalName, input)
		approved, dispatchErr := h.channelDispatcher.DispatchApproval(ctx, ApprovalDispatchRequest{
			AudienceID: h.audienceID,
			RunID:      runID,
			PolicyID:   h.policyID,
			ToolName:   internalName,
			Prompt:     prompt,
			ExpiresAt:  expiresAtTime,
		})
		if errors.Is(dispatchErr, ErrApprovalRouteToInApp) {
			// Audience resolved to in-app; fall through to the approvalCh select.
		} else if dispatchErr != nil {
			return fmt.Errorf("plugin approval dispatch for tool %s: %w", internalName, dispatchErr)
		} else {
			// Plugin resolved — update approval_requests + emit SSE before branching.
			h.resolveApprovalRecord(ctx, runID, approvalID, approved)
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
			return nil
		}
	}

	// --- In-app approval path (unchanged) ---
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
		// Race the timeout scanner for the pending row (#505): claimRequestTimeout
		// owns the rows==1/rows==0 branch and the error-step write.
		return claimRequestTimeout(ctx, h.audit, timeoutClaim{
			name:      "approval",
			runID:     runID,
			requestID: approvalID,
			claim: func(dbCtx context.Context, now string) (int64, error) {
				return h.sm.Queries().UpdateApprovalRequestStatus(dbCtx, db.UpdateApprovalRequestStatusParams{
					Status:    string(model.ApprovalStatusTimeout),
					DecidedAt: &now,
					Note:      nil,
					ID:        approvalID,
				})
			},
			errorCode:   model.ErrorCodeApprovalRejected,
			wonMessage:  fmt.Sprintf("approval timeout for tool %s", internalName),
			lostMessage: fmt.Sprintf("approval timeout for tool %s: already resolved by scanner", internalName),
		})
	case <-ctx.Done():
		return fmt.Errorf("context cancelled waiting for approval: %w", ctx.Err())
	}

	return nil
}

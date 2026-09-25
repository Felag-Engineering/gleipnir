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

	// gatesOpened counts how many gates THIS handler has opened via Wait. It
	// exists solely to skip the stale-value drain (issue #961 review R2) on
	// the very FIRST gate — see Wait's own comment for why a first gate has
	// nothing to have gone stale, and why draining unconditionally there
	// would consume a value tests throughout this package (and, in principle,
	// a caller) legitimately pre-load before that first call. No locking:
	// one run drives its ApprovalHandler's Wait calls sequentially, one gate
	// at a time, never concurrently.
	gatesOpened int
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

// resolveApprovalRecord CASes the approval_requests row to its terminal
// status and emits the approval.resolved SSE event, then reports whether the
// decision this call carries is the one that is actually taking effect.
//
// won=false means either the timeout scanner already claimed the row first
// (rows==0 on the conditional UPDATE) or the write itself failed — in BOTH
// cases the caller must fail the call rather than run the tool, mirroring
// inputrequired.go's rows==0 handling, which this call site previously did
// not (a plugin-routed approval used to run the tool regardless of whether
// the scanner had already timed the gate out).
//
// A DB write error fails closed (issue #961 review R3), unlike
// resolveFeedbackRecord's identical-looking write below: approval grants
// PERMISSION (ADR-008's hard runtime guarantee — a tool that must not run
// without a real yes), so a write this function cannot confirm succeeded is
// indistinguishable, from here, from one that lost to the scanner — there is
// no third bucket for "probably fine". A feedback answer carries no such
// stakes: it is text the agent goes on to reason about, not a grant, so
// resolveFeedbackRecord treats the same kind of write error as best-effort
// and still lets the run resume with the answer already in hand.
func (h *ApprovalHandler) resolveApprovalRecord(ctx context.Context, runID, approvalID string, approved bool) (won bool) {
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
		logctx.Logger(ctx).WarnContext(ctx, "plugin approval: UpdateApprovalRequestStatus failed; failing closed",
			"approval_id", approvalID, "run_id", runID, "err", err)
		return false
	}
	if rows == 0 {
		// Scanner already resolved this row (e.g. timeout beat the callback).
		logctx.Logger(ctx).WarnContext(ctx, "plugin approval: lost the race with the timeout scanner",
			"approval_id", approvalID, "run_id", runID)
		return false
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
	return true
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
	// Drain any stale value already sitting in approvalCh before opening this
	// gate (issue #961 review R2) — but only from the SECOND gate this
	// handler ever opens onward (h.gatesOpened > 0). approvalCh is a single
	// reusable, buffered (cap 1) channel shared across every approval gate a
	// run's agent goroutine ever opens, one at a time — never per-gate like
	// the feedback path's per-request waiter map. A value can be left
	// buffered here from a PREVIOUS gate in a few ways: SendApproval's
	// non-blocking send can land after that gate's own Wait call has already
	// returned via a timeout or context cancellation (nobody was left to
	// read it), or — before R1's runs_handler.go fix — a UI submission for an
	// already-closed gate could write here with nothing waiting to consume
	// it either. Without this drain, that stale value would be read as the
	// ANSWER to the NEXT gate this call opens, deciding it before anyone has
	// actually decided anything.
	//
	// The FIRST gate is deliberately exempt: approvalCh is created fresh per
	// run (RunLauncher, launcher.go) and nothing can legitimately write to it
	// before this handler's very first Transition — SendApproval requires a
	// PENDING approval_requests row to exist, which only Wait itself creates
	// — so a value present at that moment cannot be stale. Draining
	// unconditionally would instead consume it as if it were, which is
	// exactly the shape of the "pre-load the channel, then call Wait" setup
	// this package's own tests rely on throughout.
	if h.gatesOpened > 0 {
		select {
		case stale := <-h.approvalCh:
			logctx.Logger(ctx).WarnContext(ctx, "approval: drained a stale buffered value before opening a new gate",
				"run_id", runID, "tool", internalName, "stale_value", stale)
		default:
		}
	}
	h.gatesOpened++

	approvalID := model.NewULID()

	proposedInput, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshalling proposed input for approval request: %w", err)
	}

	// Computed ONCE, here, so the DB row's expires_at (including the
	// no-timeout default) and the deadline handed to a plugin dispatcher can
	// never diverge — a prior version derived them from two separate
	// time.Now() calls and left ApprovalDispatchRequest.ExpiresAt nil in the
	// default-timeout case, letting a plugin-routed wait run for longer than
	// the timeout scanner actually allowed the gate.
	var deadline time.Time
	if entry.tool.Timeout > 0 {
		deadline = time.Now().UTC().Add(entry.tool.Timeout)
	} else {
		deadline = time.Now().UTC().Add(time.Hour)
	}
	expiresAt := deadline.Format(time.RFC3339Nano)

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
		prompt := formatApprovalPrompt(internalName, input)
		settlement, dispatchErr := h.channelDispatcher.DispatchApproval(ctx, ApprovalDispatchRequest{
			AudienceID: h.audienceID,
			RunID:      runID,
			PolicyID:   h.policyID,
			ToolName:   internalName,
			Prompt:     prompt,
			ExpiresAt:  &deadline,
		})
		if errors.Is(dispatchErr, ErrApprovalRouteToInApp) {
			// Audience resolved to in-app; fall through to the approvalCh select.
		} else if dispatchErr != nil {
			return fmt.Errorf("plugin approval dispatch for tool %s: %w", internalName, dispatchErr)
		} else {
			// Plugin resolved — CAS approval_requests + emit SSE before
			// branching, and only THEN let the dispatcher record its own
			// decision evidence (settlement.Settle), so a decision that lost
			// the race with the scanner is never recorded as one that won.
			won := h.resolveApprovalRecord(ctx, runID, approvalID, settlement.Approved)
			if settlement.Settle != nil {
				settlement.Settle(ctx, won)
			}
			if !won {
				err := fmt.Errorf("tool call %s: the approval window already closed before this decision arrived", internalName)
				logAuditError(ctx, h.audit, Step{
					RunID:   runID,
					Type:    model.StepTypeError,
					Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeApprovalRejected},
				})
				return err
			}
			if !settlement.Approved {
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

	// --- In-app approval path ---
	// This branch does not re-check the approval_requests row itself — see
	// internal/execution/run/runs_handler.go's SubmitApproval, which is the
	// SOLE writer for this path and now CASes the row to its terminal status
	// BEFORE it ever sends on approvalCh (issue #961 review item 1c: a
	// decision that already lost the race with the timeout scanner is never
	// even forwarded here). Adding a second read or write in this branch
	// would either race runs_handler.go's own CAS over the identical value
	// or require every existing unit test that drives this path directly via
	// approvalCh (bypassing the HTTP layer entirely) to also fabricate a
	// matching DB row — the same two-writer tradeoff
	// FeedbackHandler.Wait's in-app branch already documents for
	// resolveFeedbackRecord.
	//
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

package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
)

// approvalChannelRequester is the narrow interface the adapter uses for
// testability — only the two Dispatcher methods it calls are required.
// *dispatch.Dispatcher satisfies this structurally.
type approvalChannelRequester interface {
	Request(ctx context.Context, audienceID string, rc dispatch.RouteContext, prompt string, expiresAt *time.Time) (string, dispatch.RoutingOutcome, error)
	Wait(ctx context.Context, requestID string, timeout time.Duration) (string, error)
}

// ApprovalChannelAdapter implements agent.ApprovalChannelDispatcher by
// delegating to a Dispatcher.Request + Wait pair.  It lives in the run package
// because that package already imports both agent and dispatch — placing it
// here avoids a new import cycle while keeping it independently testable.
type ApprovalChannelAdapter struct {
	d approvalChannelRequester
}

// NewApprovalChannelAdapter constructs an ApprovalChannelAdapter.
// d is typically a *dispatch.Dispatcher; tests inject a stub via the interface.
func NewApprovalChannelAdapter(d approvalChannelRequester) *ApprovalChannelAdapter {
	return &ApprovalChannelAdapter{d: d}
}

// DispatchApproval routes an approval request through the plugin channel and
// blocks until the operator responds, the timeout fires, or ctx is cancelled.
//
//   - Returns (true, nil) when the operator approves.
//   - Returns (false, nil) when the operator denies.
//   - Returns (false, agent.ErrApprovalRouteToInApp) when the audience resolves
//     to the synthetic in-app entry — the caller falls through to the approvalCh
//     path.
//   - Returns (false, err) on any other dispatch or wait failure.
func (a *ApprovalChannelAdapter) DispatchApproval(ctx context.Context, req agent.ApprovalDispatchRequest) (bool, error) {
	// Derive the Wait timeout from ExpiresAt.  The caller computes ExpiresAt
	// once; the adapter owns the derivation so the API surface stays minimal.
	var waitTimeout time.Duration
	if req.ExpiresAt != nil {
		waitTimeout = time.Until(*req.ExpiresAt)
	}
	if waitTimeout <= 0 {
		waitTimeout = time.Hour // sensible default when no expiry is set
	}

	rc := dispatch.RouteContext{
		RunID:    req.RunID,
		PolicyID: req.PolicyID,
		ToolName: req.ToolName,
	}

	reqID, outcome, err := a.d.Request(ctx, req.AudienceID, rc, req.Prompt, req.ExpiresAt)
	if err != nil {
		if errors.Is(err, dispatch.ErrNoRequestCapableEntry) {
			return false, agent.ErrApprovalRouteToInApp
		}
		return false, fmt.Errorf("channel request: %w", err)
	}
	if outcome == dispatch.RouteToInApp {
		return false, agent.ErrApprovalRouteToInApp
	}

	responseJSON, err := a.d.Wait(ctx, reqID, waitTimeout)
	if err != nil {
		return false, fmt.Errorf("waiting for approval response: %w", err)
	}

	approved, err := ParseApprovalDecision(responseJSON)
	if err != nil {
		return false, fmt.Errorf("parsing approval decision: %w", err)
	}
	return approved, nil
}

// approvalDecisionBody is the wire format for plugin-channel approval responses.
// Slack plugin sends: {"option_id":"approve","value":"approve","request_id":"...","user":"U..."}.
// The decision field is a fallback for plugins that use a simpler format.
type approvalDecisionBody struct {
	OptionID string `json:"option_id"`
	Value    string `json:"value"`
	Decision string `json:"decision"`
}

// ParseApprovalDecision interprets the JSON response from a plugin channel
// approval callback. Accepts two formats:
//   - Slack-style: {"option_id":"approve"|"reject", ...}
//   - Simple: {"decision":"approved"|"denied"}
//
// Exported so the table-driven test in the external test package can target it
// directly without going through DispatchApproval.
func ParseApprovalDecision(responseJSON string) (bool, error) {
	var body approvalDecisionBody
	if err := json.Unmarshal([]byte(responseJSON), &body); err != nil {
		return false, fmt.Errorf("invalid approval response JSON: %w", err)
	}

	// Slack-style: option_id is "approve" or "reject"
	if body.OptionID != "" {
		switch body.OptionID {
		case "approve":
			return true, nil
		case "reject":
			return false, nil
		default:
			return false, fmt.Errorf("unknown approval option_id %q: want approve or reject", body.OptionID)
		}
	}

	// Fallback: decision field
	if body.Decision != "" {
		switch body.Decision {
		case "approved":
			return true, nil
		case "denied":
			return false, nil
		default:
			return false, fmt.Errorf("unknown approval decision %q: want approved or denied", body.Decision)
		}
	}

	return false, fmt.Errorf("approval response missing both option_id and decision fields")
}

package run

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
)

// feedbackChannelRequester is the narrow interface the adapter uses for
// testability — only the two Dispatcher methods it calls are required.
// *dispatch.Dispatcher satisfies this structurally.
type feedbackChannelRequester interface {
	Request(ctx context.Context, audienceID string, rc dispatch.RouteContext, prompt string, expiresAt *time.Time) (string, dispatch.RoutingOutcome, error)
	Wait(ctx context.Context, requestID string, timeout time.Duration) (string, error)
}

// FeedbackChannelAdapter implements agent.FeedbackChannelDispatcher by
// delegating to a Dispatcher.Request + Wait pair.  It lives in the run package
// because that package already imports both agent and dispatch — placing it here
// avoids a new import cycle while keeping it independently testable.
type FeedbackChannelAdapter struct {
	d feedbackChannelRequester
}

// NewFeedbackChannelAdapter constructs a FeedbackChannelAdapter.
// d is typically a *dispatch.Dispatcher; tests inject a stub via the interface.
func NewFeedbackChannelAdapter(d feedbackChannelRequester) *FeedbackChannelAdapter {
	return &FeedbackChannelAdapter{d: d}
}

// DispatchFeedback routes a feedback request through the plugin channel and
// blocks until the operator replies, the timeout fires, or ctx is cancelled.
//
//   - Returns (Settlement{Response: responseJSON}, nil) when the operator
//     replied; the caller must call parseFeedbackResponse to extract the text
//     from the JSON envelope.
//   - Returns (_, agent.ErrFeedbackRouteToInApp) when the audience resolves to
//     the synthetic in-app entry — the caller falls through to the waiter path.
//   - Returns (_, err) on any other dispatch or wait failure.
//
// Settle is always nil: this v1 adapter has no decision-record concept.
func (a *FeedbackChannelAdapter) DispatchFeedback(ctx context.Context, req agent.FeedbackDispatchRequest) (agent.FeedbackSettlement, error) {
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
		// "mode":"feedback" tells the Slack plugin to use the threaded-reply UX
		// instead of button blocks.  Injected transiently — not persisted in the
		// audience entry's config_json and not declared in the manifest ConfigSchema.
		Metadata: map[string]string{"mode": "feedback"},
	}

	reqID, outcome, err := a.d.Request(ctx, req.AudienceID, rc, req.Prompt, req.ExpiresAt)
	if err != nil {
		if errors.Is(err, dispatch.ErrNoRequestCapableEntry) {
			return agent.FeedbackSettlement{}, agent.ErrFeedbackRouteToInApp
		}
		return agent.FeedbackSettlement{}, fmt.Errorf("channel request: %w", err)
	}
	if outcome == dispatch.RouteToInApp {
		return agent.FeedbackSettlement{}, agent.ErrFeedbackRouteToInApp
	}

	responseJSON, err := a.d.Wait(ctx, reqID, waitTimeout)
	if err != nil {
		return agent.FeedbackSettlement{}, fmt.Errorf("waiting for feedback response: %w", err)
	}
	return agent.FeedbackSettlement{Response: responseJSON}, nil
}

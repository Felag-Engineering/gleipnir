// Package agent — this file defines the internal channel abstraction used by
// FeedbackHandler to route feedback requests. See plugin-system-spec.md §4.2.
package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// ErrUnknownRequestID is returned by inAppChannel.Resolve when the given
// request_id has no registered waiter. This is the precursor to the
// plugin-system-spec §4.2 feedback_response_late hard-rejection rule; #179
// surfaces this through SubmitFeedback.
var ErrUnknownRequestID = errors.New("unknown request_id: no waiter registered")

// inAppResponse wraps the operator's response text. The struct wrapper preserves
// forward-compat for #179 (e.g. adding metadata without changing the channel type).
type inAppResponse struct {
	text string
}

// channel is the internal service contract that each feedback channel must
// satisfy. It mirrors the plugin-spec §4.2 ChannelService surface so that
// #178/#179 can plug in alternative implementations without touching
// FeedbackHandler.
type channel interface {
	// Notify sends a fire-and-forget notification (no operator response expected).
	Notify(ctx context.Context, req notifyRequest) error

	// Request suspends the run and waits for an operator response.
	Request(ctx context.Context, req feedbackRequest) (string, error)
}

// notifyRequest carries the arguments for a one-way notification.
type notifyRequest struct {
	RunID   string
	Message string
}

// feedbackRequest carries the arguments for a blocking operator-response request.
type feedbackRequest struct {
	RunID          string
	FeedbackID     string
	ToolName       string
	ProposedInput  string // maps to the inputJSON argument in Wait; only used in state-machine payload
	Message        string
	Timeout        time.Duration
	ExpiresAt      string
}

// channelDispatcher routes feedback calls to the configured channel
// implementation. Today every call goes to the single hard-wired in-app
// implementation; future routing (#178+) will pick a channel by policy
// audience without touching FeedbackHandler.
type channelDispatcher struct {
	inApp channel
}

func newChannelDispatcher(inApp channel) *channelDispatcher {
	return &channelDispatcher{inApp: inApp}
}

func (d *channelDispatcher) Notify(ctx context.Context, req notifyRequest) error {
	return d.inApp.Notify(ctx, req)
}

func (d *channelDispatcher) Request(ctx context.Context, req feedbackRequest) (string, error) {
	return d.inApp.Request(ctx, req)
}

// localFeedbackChannel is the legacy in-app channel implementation that routes
// feedback through the runtime channel (feedbackCh). Superseded by inAppChannel
// in #179. DEAD: kept for #180 parallel-impl harness; #181 removes.
type localFeedbackChannel struct { // DEAD: kept for #180 parallel-impl harness; #181 removes.
	audit      *AuditWriter
	sm         *RunStateMachine
	feedbackCh <-chan string
}

// Notify is a no-op for the in-app channel — in-app operators see the
// feedback_request step directly in the UI, so there is no separate
// notification path. This satisfies the channel interface without a missing
// implementation; #178 will introduce a real Notify for external channels.
func (c *localFeedbackChannel) Notify(_ context.Context, _ notifyRequest) error {
	return nil
}

// Request contains the Wait body verbatim (behavior-neutral move, ADR-031).
// Keep this as one cohesive block — #180 needs a single contract surface to wrap.
func (c *localFeedbackChannel) Request(ctx context.Context, req feedbackRequest) (string, error) {
	if err := c.audit.Write(ctx, Step{
		RunID: req.RunID,
		Type:  model.StepTypeFeedbackRequest,
		Content: map[string]any{
			"feedback_id": req.FeedbackID,
			"tool":        req.ToolName,
			"message":     req.Message,
			"expires_at":  req.ExpiresAt,
		},
	}); err != nil {
		return "", fmt.Errorf("writing feedback_request step: %w", err)
	}

	if err := c.sm.Transition(ctx, model.RunStatusWaitingForFeedback, "", WithFeedbackPayload(FeedbackPayload{
		FeedbackID:    req.FeedbackID,
		ToolName:      req.ToolName,
		ProposedInput: req.ProposedInput,
		Message:       req.Message,
		ExpiresAt:     req.ExpiresAt,
	})); err != nil {
		return "", fmt.Errorf("transitioning run to waiting_for_feedback: %w", err)
	}

	// nil timeoutCh (when Timeout == 0) blocks forever in the select,
	// meaning no in-process timeout is applied. Use NewTimer so we can Stop it
	// on early response — time.After leaks until the duration fires.
	var timeoutCh <-chan time.Time
	if req.Timeout > 0 {
		timer := time.NewTimer(req.Timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case responseText := <-c.feedbackCh:
		if err := c.audit.Write(ctx, Step{
			RunID:   req.RunID,
			Type:    model.StepTypeFeedbackResponse,
			Content: map[string]any{"feedback_id": req.FeedbackID, "response": responseText},
		}); err != nil {
			return "", fmt.Errorf("writing feedback_response step: %w", err)
		}
		if err := c.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
			return "", fmt.Errorf("transitioning run back to running after feedback: %w", err)
		}
		return responseText, nil
	case <-timeoutCh:
		logctx.Logger(ctx).WarnContext(ctx, "feedback timeout reached",
			"tool", req.ToolName,
			"timeout", req.Timeout.String())
		now := time.Now().UTC().Format(time.RFC3339Nano)
		// Race the scanner: only the first writer (rows==1) owns the error step.
		// If the scanner already resolved it (rows==0), return a sentinel error so
		// Run() still terminates, but skip logAuditError to avoid a duplicate step.
		rows, dbErr := c.sm.Queries().UpdateFeedbackRequestStatus(
			context.Background(),
			db.UpdateFeedbackRequestStatusParams{
				Status:     "timed_out",
				Response:   nil,
				ResolvedAt: &now,
				ID:         req.FeedbackID,
			},
		)
		if dbErr != nil {
			logctx.Logger(ctx).WarnContext(ctx, "failed to update feedback status on timeout", "feedback_id", req.FeedbackID, "err", dbErr)
		}
		if rows == 1 {
			err := fmt.Errorf("feedback timeout: operator did not respond within %s", req.Timeout)
			logAuditError(ctx, c.audit, Step{
				RunID:   req.RunID,
				Type:    model.StepTypeError,
				Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeFeedbackTimeout},
			})
			return "", err
		}
		// Scanner won the race: it already wrote the error step and transitioned
		// the run. Return a sentinel so Run() knows to stop, but avoid a duplicate step.
		logctx.Logger(ctx).DebugContext(ctx, "feedback already resolved by scanner", "feedback_id", req.FeedbackID)
		return "", fmt.Errorf("feedback timeout: already resolved by scanner for tool %s", req.ToolName)
	case <-ctx.Done():
		return "", fmt.Errorf("context cancelled waiting for feedback: %w", ctx.Err())
	}
}

// inAppChannel is the new in-process feedback channel implementation. It stores
// pending requests in a waiter map keyed by feedback_id and routes Resolve calls
// back to the blocking Request call. It satisfies the channel interface and is a
// sibling of localFeedbackChannel — the swap of localFeedbackChannel for
// inAppChannel in NewFeedbackHandler lands atomically in #179.
type inAppChannel struct {
	audit   *AuditWriter
	sm      *RunStateMachine
	mu      sync.Mutex
	waiters map[string]chan inAppResponse
}

// newInAppChannel constructs an inAppChannel. audit and sm must not be nil.
func newInAppChannel(audit *AuditWriter, sm *RunStateMachine) *inAppChannel {
	return &inAppChannel{
		audit:   audit,
		sm:      sm,
		waiters: make(map[string]chan inAppResponse),
	}
}

// Notify is a no-op for the in-app channel. In-app operators see the
// feedback_request step directly in the UI; no separate notification is needed.
func (c *inAppChannel) Notify(_ context.Context, _ notifyRequest) error {
	return nil
}

// Request registers a waiter for req.FeedbackID, transitions the run to
// waiting_for_feedback, then blocks until Resolve delivers a response, the
// timeout fires, or the context is cancelled.
//
// The waiter is registered BEFORE the state transition so that any concurrent
// Resolve call arriving immediately after the SSE event (which fires on transition)
// is never lost — relevant once #179 wires SubmitFeedback to Resolve.
func (c *inAppChannel) Request(ctx context.Context, req feedbackRequest) (string, error) {
	// 1. Allocate the buffered channel first (cap 1 — Resolve never blocks).
	ch := make(chan inAppResponse, 1)

	// 2. Register BEFORE state transition so an immediate Resolve cannot miss us.
	c.mu.Lock()
	c.waiters[req.FeedbackID] = ch
	c.mu.Unlock()

	// 3. Deferred unregister: if we return for any reason (timeout, ctx cancel,
	//    or after a successful response), remove the waiter entry so a late
	//    Resolve gets ErrUnknownRequestID rather than sending to a leaked channel.
	defer func() {
		c.mu.Lock()
		delete(c.waiters, req.FeedbackID)
		c.mu.Unlock()
	}()

	// 4. Write the audit step.
	if err := c.audit.Write(ctx, Step{
		RunID: req.RunID,
		Type:  model.StepTypeFeedbackRequest,
		Content: map[string]any{
			"feedback_id": req.FeedbackID,
			"tool":        req.ToolName,
			"message":     req.Message,
			"expires_at":  req.ExpiresAt,
		},
	}); err != nil {
		return "", fmt.Errorf("writing feedback_request step: %w", err)
	}

	// 5. Transition to waiting_for_feedback.
	if err := c.sm.Transition(ctx, model.RunStatusWaitingForFeedback, "", WithFeedbackPayload(FeedbackPayload{
		FeedbackID:    req.FeedbackID,
		ToolName:      req.ToolName,
		ProposedInput: req.ProposedInput,
		Message:       req.Message,
		ExpiresAt:     req.ExpiresAt,
	})); err != nil {
		return "", fmt.Errorf("transitioning run to waiting_for_feedback: %w", err)
	}

	// nil timeoutCh (when Timeout == 0) blocks forever in the select,
	// meaning no in-process timeout is applied. Use NewTimer so we can Stop it
	// on early response — time.After leaks until the duration fires.
	var timeoutCh <-chan time.Time
	if req.Timeout > 0 {
		timer := time.NewTimer(req.Timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	// 6. Block until one of the three arms fires.
	select {
	case resp := <-ch:
		if err := c.audit.Write(ctx, Step{
			RunID:   req.RunID,
			Type:    model.StepTypeFeedbackResponse,
			Content: map[string]any{"feedback_id": req.FeedbackID, "response": resp.text},
		}); err != nil {
			return "", fmt.Errorf("writing feedback_response step: %w", err)
		}
		if err := c.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
			return "", fmt.Errorf("transitioning run back to running after feedback: %w", err)
		}
		return resp.text, nil
	case <-timeoutCh:
		logctx.Logger(ctx).WarnContext(ctx, "feedback timeout reached",
			"tool", req.ToolName,
			"timeout", req.Timeout.String())
		now := time.Now().UTC().Format(time.RFC3339Nano)
		// Race the scanner: only the first writer (rows==1) owns the error step.
		// If the scanner already resolved it (rows==0), return a sentinel error so
		// Run() still terminates, but skip logAuditError to avoid a duplicate step.
		rows, dbErr := c.sm.Queries().UpdateFeedbackRequestStatus(
			context.Background(),
			db.UpdateFeedbackRequestStatusParams{
				Status:     "timed_out",
				Response:   nil,
				ResolvedAt: &now,
				ID:         req.FeedbackID,
			},
		)
		if dbErr != nil {
			logctx.Logger(ctx).WarnContext(ctx, "failed to update feedback status on timeout", "feedback_id", req.FeedbackID, "err", dbErr)
		}
		if rows == 1 {
			err := fmt.Errorf("feedback timeout: operator did not respond within %s", req.Timeout)
			logAuditError(ctx, c.audit, Step{
				RunID:   req.RunID,
				Type:    model.StepTypeError,
				Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeFeedbackTimeout},
			})
			return "", err
		}
		// Scanner won the race: it already wrote the error step and transitioned
		// the run. Return a sentinel so Run() knows to stop, but avoid a duplicate step.
		logctx.Logger(ctx).DebugContext(ctx, "feedback already resolved by scanner", "feedback_id", req.FeedbackID)
		return "", fmt.Errorf("feedback timeout: already resolved by scanner for tool %s", req.ToolName)
	case <-ctx.Done():
		return "", fmt.Errorf("context cancelled waiting for feedback: %w", ctx.Err())
	}
}

// Resolve delivers a response to the waiter registered for requestID.
//
// The ch <- send is intentionally OUTSIDE the lock. ch is buffered (cap 1) and
// has a single reader (Request's select), so the send never blocks. Moving the
// send inside the lock would serialize all Resolve calls and risk deadlock if a
// future change makes the channel unbuffered. Do not "optimize" by hoisting the
// send into the lock.
func (c *inAppChannel) Resolve(requestID, body string) error {
	c.mu.Lock()
	ch, ok := c.waiters[requestID]
	if ok {
		delete(c.waiters, requestID)
	}
	c.mu.Unlock()

	if !ok {
		return ErrUnknownRequestID
	}
	ch <- inAppResponse{text: body}
	return nil
}

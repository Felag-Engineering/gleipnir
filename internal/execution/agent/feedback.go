// Package agent — this file implements FeedbackHandler, which manages the
// gleipnir.ask_operator synthetic tool. It holds no BoundAgent reference so it
// can be constructed and tested independently (ADR-031).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// SyntheticToolPrefix is the namespace prefix for tools injected by the Gleipnir
// runtime. These tools are never registered in the MCP registry and must never
// be dispatched to an MCP server.
const SyntheticToolPrefix = "gleipnir."

// AskOperatorToolName is the name of the synthetic feedback tool the agent can
// call to pause the run and wait for a human operator response.
const AskOperatorToolName = "gleipnir.ask_operator"

// askOperatorSchema is the static JSON schema for the gleipnir.ask_operator tool.
var askOperatorSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "reason": {"type": "string", "description": "Why the agent is asking. Displayed as the headline in the UI."},
    "context": {"type": "string", "description": "Supporting detail the operator might need to make a decision."}
  },
  "required": ["reason"]
}`)

// askOperatorToolDefinition returns the provider-neutral tool definition for the
// gleipnir.ask_operator synthetic tool.
func askOperatorToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        AskOperatorToolName,
		Description: "Ask the human operator a question. The run will pause until the operator responds with freeform text.",
		InputSchema: askOperatorSchema,
	}
}

// FeedbackHandler manages the gleipnir.ask_operator synthetic tool call lifecycle.
// It suspends the run, waits for an operator response, and resumes it. It holds
// no BoundAgent reference — wire it via NewFeedbackHandler and pass it to BoundAgent.
type FeedbackHandler struct {
	audit          *AuditWriter
	sm             *RunStateMachine
	defaultTimeout time.Duration
	inApp          *inAppChannel

	// Plugin channel routing — populated by WithFeedbackChannelDispatch.
	// When non-nil and audienceID is non-empty, Wait routes feedback through the
	// plugin channel instead of blocking on the inApp waiter.
	channelDispatcher FeedbackChannelDispatcher
	policyID          string
	audienceID        string
}

// FeedbackHandlerOption is a functional option for NewFeedbackHandler.
type FeedbackHandlerOption func(*FeedbackHandler)

// WithFeedbackChannelDispatch attaches a plugin channel dispatcher to the
// handler.  When d is non-nil and audienceID is non-empty, Wait routes the
// feedback request through the plugin channel instead of waiting on the in-app
// waiter channel.
func WithFeedbackChannelDispatch(d FeedbackChannelDispatcher, audienceID, policyID string) FeedbackHandlerOption {
	return func(h *FeedbackHandler) {
		h.channelDispatcher = d
		h.audienceID = audienceID
		h.policyID = policyID
	}
}

// NewFeedbackHandler constructs a FeedbackHandler.
// Optional functional opts (e.g. WithFeedbackChannelDispatch) are applied
// after initialization; existing callers that pass zero opts are unaffected.
func NewFeedbackHandler(audit *AuditWriter, sm *RunStateMachine, defaultTimeout time.Duration, opts ...FeedbackHandlerOption) *FeedbackHandler {
	inApp := newInAppChannel(audit, sm)
	h := &FeedbackHandler{
		audit:          audit,
		sm:             sm,
		defaultTimeout: defaultTimeout,
		inApp:          inApp,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Resolve delivers an operator response to the in-app waiter for requestID.
// Returns agent.ErrUnknownRequestID if no waiter is currently registered
// (timed out, already answered, or the run was cancelled). Callers should treat
// that as a benign late-callback signal, not an error.
//
// When the plugin dispatch path is active, Resolve returns ErrUnknownRequestID
// because no in-app waiter is registered — the plugin path uses
// dispatch.Dispatcher.Resolve (via hostsvc WriteAuditStep) instead.
func (h *FeedbackHandler) Resolve(requestID, body string) error {
	return h.inApp.Resolve(requestID, body)
}

// resolveFeedbackRecord marks the feedback_requests DB row as resolved and
// emits the feedback.resolved SSE event.  Both operations are best-effort:
// the agent goroutine must resume regardless of DB or marshal errors.  SSE is
// gated on the CAS winning (rows > 0) — if the scanner already timed out the
// row, the SSE must not be emitted for a stale decision.
func (h *FeedbackHandler) resolveFeedbackRecord(ctx context.Context, runID, feedbackID, responseText string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := h.sm.Queries().UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
		Status:     "resolved",
		Response:   &responseText,
		ResolvedAt: &now,
		ID:         feedbackID,
	})
	if err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "plugin feedback: UpdateFeedbackRequestStatus failed",
			"feedback_id", feedbackID, "run_id", runID, "err", err)
		return
	}
	if rows == 0 {
		// Scanner already resolved this row (e.g. timeout beat the callback).
		logctx.Logger(ctx).DebugContext(ctx, "feedback already resolved by scanner",
			"feedback_id", feedbackID, "run_id", runID)
		return
	}

	if pub := h.sm.Publisher(); pub != nil {
		payload := map[string]string{
			"feedback_id": feedbackID,
			"run_id":      runID,
			"status":      "resolved",
		}
		if data, marshalErr := json.Marshal(payload); marshalErr == nil {
			pub.Publish("feedback.resolved", data)
		}
	}
}

// parseFeedbackResponse extracts the "text" field from a JSON response string.
// The plugin encodes the reply as {"text":"...", "user":"...", "request_id":"..."}.
// On parse failure or missing "text", logs a warning and returns responseJSON
// as-is so the agent always resumes with something meaningful.
func parseFeedbackResponse(responseJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(responseJSON), &m); err != nil {
		slog.Warn("feedback response JSON parse failed, using raw response", "err", err)
		return responseJSON
	}
	text, ok := m["text"].(string)
	if !ok {
		slog.Warn("feedback response JSON missing 'text' field, using raw response")
		return responseJSON
	}
	return text
}

// Wait suspends the run waiting for a freeform operator response.
//
// Phase 1 (always runs): write the feedback_request audit step, transition to
// waiting_for_feedback (which creates the DB record and publishes feedback.created).
//
// Phase 2 branches on whether a plugin channel dispatcher is configured:
//
//   - Plugin path: delegates to channelDispatcher.DispatchFeedback and blocks
//     until the Slack plugin calls WriteAuditStep(feedback_response) which triggers
//     dispatch.Dispatcher.Resolve → Wait unblocks.  The feedback_response audit
//     step is written by hostsvc, NOT here (hostsvc already writes it, see
//     BLOCKING #4 in the implementation plan).
//
//   - In-app path (fallback): blocks on the inApp waiter channel, writes the
//     feedback_response audit step here (preserving existing behavior).
func (h *FeedbackHandler) Wait(ctx context.Context, runID, toolName, inputJSON, mcpOutput string, feedbackTimeout time.Duration) (string, error) {
	feedbackID := model.NewULID()

	// Compute expires_at so the DB record and the audit step both carry the deadline.
	// An empty string means no timeout — the DB scanner ignores rows with NULL expires_at.
	var expiresAt string
	if feedbackTimeout > 0 {
		expiresAt = time.Now().UTC().Add(feedbackTimeout).Format(time.RFC3339Nano)
	}

	// Pre-register the in-app waiter BEFORE the state transition.  This preserves
	// the invariant from the original inAppChannel.Request: a Resolve call arriving
	// immediately after the SSE event (which fires on transition) is never lost.
	// The waiter handle bundles registration with its cleanup; on the plugin path
	// the waiter is unused; release is deferred so it is cleaned up regardless of
	// which branch executes.
	waiter := h.inApp.registerWaiter(feedbackID)
	defer waiter.release()

	// Phase 1: write audit step and transition — always runs regardless of dispatch path.
	if err := h.audit.Write(ctx, Step{
		RunID: runID,
		Type:  model.StepTypeFeedbackRequest,
		Content: map[string]any{
			"feedback_id": feedbackID,
			"tool":        toolName,
			"message":     mcpOutput,
			"expires_at":  expiresAt,
		},
	}); err != nil {
		return "", fmt.Errorf("writing feedback_request step: %w", err)
	}

	if err := h.sm.Transition(ctx, model.RunStatusWaitingForFeedback, "", WithFeedbackPayload(FeedbackPayload{
		FeedbackID:    feedbackID,
		ToolName:      toolName,
		ProposedInput: inputJSON,
		Message:       mcpOutput,
		ExpiresAt:     expiresAt,
	})); err != nil {
		return "", fmt.Errorf("transitioning run to waiting_for_feedback: %w", err)
	}

	// Phase 2a: plugin channel path.
	if h.channelDispatcher != nil && h.audienceID != "" {
		var expiresAtTime *time.Time
		if feedbackTimeout > 0 {
			t := time.Now().UTC().Add(feedbackTimeout)
			expiresAtTime = &t
		}

		response, dispatchErr := h.channelDispatcher.DispatchFeedback(ctx, FeedbackDispatchRequest{
			AudienceID: h.audienceID,
			RunID:      runID,
			PolicyID:   h.policyID,
			ToolName:   toolName,
			Prompt:     mcpOutput,
			ExpiresAt:  expiresAtTime,
		})
		if errors.Is(dispatchErr, ErrFeedbackRouteToInApp) {
			// Audience resolved to in-app; fall through to the waiter select below.
		} else if dispatchErr != nil {
			return "", fmt.Errorf("plugin feedback dispatch: %w", dispatchErr)
		} else {
			// Plugin resolved the request.
			// NOTE: Do NOT write a feedback_response audit step here.
			// hostsvc.WriteAuditStep (called by the Slack plugin) has already written
			// it — writing another would create a duplicate (BLOCKING #4).
			parsedText := parseFeedbackResponse(response)
			h.resolveFeedbackRecord(ctx, runID, feedbackID, parsedText)
			if err := h.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
				return "", fmt.Errorf("transitioning run back to running after plugin feedback: %w", err)
			}
			return parsedText, nil
		}
	}

	// Phase 2b: in-app waiter path (no dispatcher or fallback from plugin path).
	// The waiter was registered before Phase 1; deferred release handles cleanup.

	// nil timeoutCh (when feedbackTimeout == 0) blocks forever in the select,
	// meaning no in-process timeout is applied. Use NewTimer so we can Stop it
	// on early response — time.After leaks until the duration fires.
	var timeoutCh <-chan time.Time
	if feedbackTimeout > 0 {
		timer := time.NewTimer(feedbackTimeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case resp := <-waiter.responses():
		if err := h.audit.Write(ctx, Step{
			RunID:   runID,
			Type:    model.StepTypeFeedbackResponse,
			Content: map[string]any{"feedback_id": feedbackID, "response": resp.text},
		}); err != nil {
			return "", fmt.Errorf("writing feedback_response step: %w", err)
		}
		if err := h.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
			return "", fmt.Errorf("transitioning run back to running after feedback: %w", err)
		}
		// Do NOT call resolveFeedbackRecord here. The HTTP SubmitFeedback handler
		// owns the pending→resolved CAS for the in-app path (step d in runs_handler.go).
		// Calling UpdateFeedbackRequestStatus from both sides creates a two-writer
		// race: whichever loses the CAS gets rows==0, and the HTTP handler would
		// return 409 to an operator whose submission was genuinely delivered.
		// resolveFeedbackRecord is only needed on the plugin-dispatched path (Phase 2a
		// above), where no HTTP handler transitions the record.
		return resp.text, nil

	case <-timeoutCh:
		logctx.Logger(ctx).WarnContext(ctx, "feedback timeout reached",
			"tool", toolName,
			"timeout", feedbackTimeout.String())
		// Race the timeout scanner for the pending row (#505): claimRequestTimeout
		// owns the rows==1/rows==0 branch and the error-step write.
		return "", claimRequestTimeout(ctx, h.audit, timeoutClaim{
			name:      "feedback",
			runID:     runID,
			requestID: feedbackID,
			claim: func(dbCtx context.Context, now string) (int64, error) {
				return h.sm.Queries().UpdateFeedbackRequestStatus(dbCtx, db.UpdateFeedbackRequestStatusParams{
					Status:     "timed_out",
					Response:   nil,
					ResolvedAt: &now,
					ID:         feedbackID,
				})
			},
			errorCode:   model.ErrorCodeFeedbackTimeout,
			wonMessage:  fmt.Sprintf("feedback timeout: operator did not respond within %s", feedbackTimeout),
			lostMessage: fmt.Sprintf("feedback timeout: already resolved by scanner for tool %s", toolName),
		})

	case <-ctx.Done():
		return "", fmt.Errorf("context cancelled waiting for feedback: %w", ctx.Err())
	}
}

// resolveOperatorTimeout resolves how long a run may sit waiting on a human.
// The policy's feedback timeout is that clock for every wait that ends at an
// operator, whether the agent asked (gleipnir.ask_operator) or a tool did
// (ADR-055) — hence one resolver rather than one per caller. def is the
// system-wide fallback; pass zero to let the calling handler apply its own.
//
// A parse error here indicates data corruption or a manual DB edit, because the
// policy validator already rejects invalid durations at save time. Log loudly
// rather than silently discarding the misconfigured value.
func resolveOperatorTimeout(ctx context.Context, feedbackCfg model.FeedbackConfig, def time.Duration) time.Duration {
	var timeout time.Duration
	if feedbackCfg.Timeout != "" {
		var parseErr error
		timeout, parseErr = time.ParseDuration(feedbackCfg.Timeout)
		if parseErr != nil {
			logctx.Logger(ctx).WarnContext(ctx, "invalid feedback timeout in policy, falling back to default",
				"timeout_value", feedbackCfg.Timeout,
				"err", parseErr)
			timeout = 0
		}
	}
	if timeout == 0 {
		return def
	}
	return timeout
}

// HandleAskOperator dispatches a gleipnir.ask_operator tool call. It validates
// the input, resolves the feedback timeout from feedbackCfg, and delegates to
// Wait. Returns (responseText, isError, err).
//
// The feedbackCfg.Enabled check is defense-in-depth (ADR-001): buildToolDefinitions
// already excludes gleipnir.ask_operator when feedback is disabled, so the LLM
// cannot call it through normal reasoning. This guard handles the case where the
// LLM hallucinates the call despite it not being in the tool list.
func (h *FeedbackHandler) HandleAskOperator(ctx context.Context, runID, toolName string, input map[string]any, feedbackCfg model.FeedbackConfig) (string, bool, error) {
	// Hard capability enforcement: reject synthetic tool calls when the
	// corresponding capability is not enabled for this policy.
	if !feedbackCfg.Enabled {
		err := fmt.Errorf("synthetic tool %s is not enabled for this policy", toolName)
		logAuditError(ctx, h.audit, Step{
			RunID:   runID,
			Type:    model.StepTypeError,
			Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeToolError},
		})
		return "", true, err
	}

	if toolName != AskOperatorToolName {
		err := fmt.Errorf("unknown synthetic tool: %s", toolName)
		logAuditError(ctx, h.audit, Step{
			RunID:   runID,
			Type:    model.StepTypeError,
			Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeToolError},
		})
		return "", true, err
	}

	// Extract required "reason" field. A missing or non-string reason still
	// fails the run here, unlike MCP/plugin tool schema violations (which
	// #744 made correctable): gleipnir.ask_operator's schema is host-authored
	// and non-negotiable, not a third-party server's contract the agent
	// might reasonably misjudge, so there is nothing for the agent to
	// self-correct toward.
	reasonRaw, ok := input["reason"]
	if !ok {
		err := fmt.Errorf("gleipnir.ask_operator: missing required field 'reason'")
		logAuditError(ctx, h.audit, Step{
			RunID:   runID,
			Type:    model.StepTypeError,
			Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeSchemaViolation},
		})
		return "", true, err
	}
	reason, ok := reasonRaw.(string)
	if !ok {
		err := fmt.Errorf("gleipnir.ask_operator: field 'reason' must be a string")
		logAuditError(ctx, h.audit, Step{
			RunID:   runID,
			Type:    model.StepTypeError,
			Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeSchemaViolation},
		})
		return "", true, err
	}

	// Extract optional "context" field.
	detail := ""
	if contextRaw, ok := input["context"]; ok {
		if s, ok := contextRaw.(string); ok {
			detail = s
		}
	}

	// Build the message sent to the feedback_request step: reason is the
	// headline; context (if present) is the supporting detail.
	message := reason
	if detail != "" {
		message += "\n\n" + detail
	}

	responseText, err := h.Wait(ctx, runID, AskOperatorToolName, "", message, resolveOperatorTimeout(ctx, feedbackCfg, h.defaultTimeout))
	if err != nil {
		return "", false, err
	}
	return responseText, false, nil
}

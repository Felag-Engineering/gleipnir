// Package agent — this file implements FeedbackHandler, which manages the
// gleipnir.ask_operator synthetic tool. It holds no BoundAgent reference so it
// can be constructed and tested independently (ADR-031).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/model"
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
	feedbackCh     <-chan string // receive-only: handler never closes the channel
	defaultTimeout time.Duration
}

// NewFeedbackHandler constructs a FeedbackHandler. feedbackCh must be receive-only
// (compile-time guarantee the handler does not close it).
func NewFeedbackHandler(audit *AuditWriter, sm *RunStateMachine, feedbackCh <-chan string, defaultTimeout time.Duration) *FeedbackHandler {
	return &FeedbackHandler{
		audit:          audit,
		sm:             sm,
		feedbackCh:     feedbackCh,
		defaultTimeout: defaultTimeout,
	}
}

// Wait suspends the run waiting for a freeform operator response.
// It transitions to waiting_for_feedback (which creates the DB record and
// publishes feedback.created), then blocks on the feedback channel, the timeout
// channel, or context cancellation. Returns the operator's response text on success.
func (h *FeedbackHandler) Wait(ctx context.Context, runID, toolName, inputJSON, mcpOutput string, feedbackTimeout time.Duration) (string, error) {
	feedbackID := model.NewULID()

	// Compute expires_at so the DB record and the audit step both carry the deadline.
	// An empty string means no timeout — the DB scanner ignores rows with NULL expires_at.
	var expiresAt string
	if feedbackTimeout > 0 {
		expiresAt = time.Now().UTC().Add(feedbackTimeout).Format(time.RFC3339Nano)
	}

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
	case responseText := <-h.feedbackCh:
		if err := h.audit.Write(ctx, Step{
			RunID:   runID,
			Type:    model.StepTypeFeedbackResponse,
			Content: map[string]any{"feedback_id": feedbackID, "response": responseText},
		}); err != nil {
			return "", fmt.Errorf("writing feedback_response step: %w", err)
		}
		if err := h.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
			return "", fmt.Errorf("transitioning run back to running after feedback: %w", err)
		}
		return responseText, nil
	case <-timeoutCh:
		slog.WarnContext(ctx, "feedback timeout reached",
			"run_id", runID, "tool", toolName,
			"timeout", feedbackTimeout.String())
		now := time.Now().UTC().Format(time.RFC3339Nano)
		// Race the scanner: only the first writer (rows==1) owns the error step.
		// If the scanner already resolved it (rows==0), return a sentinel error so
		// Run() still terminates, but skip logAuditError to avoid a duplicate step.
		rows, dbErr := h.sm.Queries().UpdateFeedbackRequestStatus(
			context.Background(),
			db.UpdateFeedbackRequestStatusParams{
				Status:     "timed_out",
				Response:   nil,
				ResolvedAt: &now,
				ID:         feedbackID,
			},
		)
		if dbErr != nil {
			slog.WarnContext(ctx, "failed to update feedback status on timeout", "feedback_id", feedbackID, "err", dbErr)
		}
		if rows == 1 {
			err := fmt.Errorf("feedback timeout: operator did not respond within %s", feedbackTimeout)
			logAuditError(ctx, h.audit, Step{
				RunID:   runID,
				Type:    model.StepTypeError,
				Content: model.ErrorStepContent{Message: err.Error(), Code: model.ErrorCodeFeedbackTimeout},
			})
			return "", err
		}
		// Scanner won the race: it already wrote the error step and transitioned
		// the run. Return a sentinel so Run() knows to stop, but avoid a duplicate step.
		slog.DebugContext(ctx, "feedback already resolved by scanner", "feedback_id", feedbackID)
		return "", fmt.Errorf("feedback timeout: already resolved by scanner for tool %s", toolName)
	case <-ctx.Done():
		return "", fmt.Errorf("context cancelled waiting for feedback: %w", ctx.Err())
	}
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

	// Extract required "reason" field. A missing or non-string reason is a
	// schema violation — fail the run, consistent with MCP schema violations.
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

	// Resolve the feedback timeout. The policy may specify a per-policy value;
	// if absent or zero, fall back to the system default.
	//
	// A parse error here indicates data corruption or a manual DB edit because
	// the policy validator already rejects invalid durations at save time. Log
	// loudly rather than silently discarding the misconfigured value.
	var feedbackTimeout time.Duration
	if feedbackCfg.Timeout != "" {
		var parseErr error
		feedbackTimeout, parseErr = time.ParseDuration(feedbackCfg.Timeout)
		if parseErr != nil {
			slog.WarnContext(ctx, "invalid feedback timeout in policy, falling back to default",
				"run_id", runID,
				"timeout_value", feedbackCfg.Timeout,
				"err", parseErr)
			feedbackTimeout = 0
		}
	}
	if feedbackTimeout == 0 {
		feedbackTimeout = h.defaultTimeout
	}

	responseText, err := h.Wait(ctx, runID, AskOperatorToolName, "", message, feedbackTimeout)
	if err != nil {
		return "", false, err
	}
	return responseText, false, nil
}

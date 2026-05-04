// Package agent — this file implements FeedbackHandler, which manages the
// gleipnir.ask_operator synthetic tool. It holds no BoundAgent reference so it
// can be constructed and tested independently (ADR-031).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	feedbackCh     <-chan string // DEAD: kept for #180 parallel-impl harness; #181 removes.
	defaultTimeout time.Duration
	inApp          *inAppChannel
	dispatcher     *channelDispatcher
}

// NewFeedbackHandler constructs a FeedbackHandler. feedbackCh is ignored in the
// production path (DEAD: kept for #180 parallel-impl harness; #181 removes).
func NewFeedbackHandler(audit *AuditWriter, sm *RunStateMachine, feedbackCh <-chan string, defaultTimeout time.Duration) *FeedbackHandler {
	inApp := newInAppChannel(audit, sm)
	return &FeedbackHandler{
		audit:          audit,
		sm:             sm,
		feedbackCh:     feedbackCh, // DEAD: kept for #180 parallel-impl harness; #181 removes.
		defaultTimeout: defaultTimeout,
		inApp:          inApp,
		dispatcher:     newChannelDispatcher(inApp),
	}
}

// Resolve delivers an operator response to the in-app waiter for requestID.
// Returns agent.ErrUnknownRequestID if no waiter is currently registered
// (timed out, already answered, or the run was cancelled). Callers should treat
// that as a benign late-callback signal, not an error.
func (h *FeedbackHandler) Resolve(requestID, body string) error {
	return h.inApp.Resolve(requestID, body)
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

	return h.dispatcher.Request(ctx, feedbackRequest{
		RunID:         runID,
		FeedbackID:    feedbackID,
		ToolName:      toolName,
		ProposedInput: inputJSON,
		Message:       mcpOutput,
		Timeout:       feedbackTimeout,
		ExpiresAt:     expiresAt,
	})
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
			logctx.Logger(ctx).WarnContext(ctx, "invalid feedback timeout in policy, falling back to default",
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

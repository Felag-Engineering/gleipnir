// Package agent implements the BoundAgent runner — the core Claude API loop
// with hard capability enforcement, approval interception, and audit writing.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// MessagesAPI is the subset of the Anthropic Messages service used by BoundAgent.
// Extracted as an interface for testing — real code uses cfg.Claude.Messages directly.
type MessagesAPI interface {
	New(ctx context.Context, body anthropic.MessageNewParams, opts ...option.RequestOption) (*anthropic.Message, error)
}

// resolvedToolEntry holds a ResolvedTool paired with its narrowed JSON schema.
type resolvedToolEntry struct {
	tool           mcp.ResolvedTool
	narrowedSchema json.RawMessage
}

// sanitizeToolName replaces any character outside [a-zA-Z0-9_-] with '_' and
// truncates to 128 characters. The Claude API rejects tool names containing
// dots or other special characters, so we must sanitize before registration.
func sanitizeToolName(name string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
	if len(sanitized) > 128 {
		sanitized = sanitized[:128]
	}
	return sanitized
}

// BoundAgent executes a single policy run. It owns the Claude API loop,
// dispatches tool calls to MCP clients, intercepts approval-gated actuators,
// and writes every step to the audit trail via AuditWriter.
type BoundAgent struct {
	policy      *model.ParsedPolicy
	tools       []mcp.ResolvedTool
	toolsByName map[string]resolvedToolEntry
	// claudeNameToInternal maps sanitized Claude-facing tool names back to the
	// internal dot-separated names used as keys in toolsByName. Required because
	// the Claude API rejects tool names containing dots.
	claudeNameToInternal map[string]string
	messages             MessagesAPI
	audit                *AuditWriter
	sm                   *RunStateMachine
	// approvalCh receives the operator's decision when a run is suspended
	// waiting_for_approval. Sent by the approval handler in internal/trigger.
	approvalCh <-chan bool
}

// Config holds the dependencies needed to construct a BoundAgent.
type Config struct {
	Policy       *model.ParsedPolicy
	Tools        []mcp.ResolvedTool
	Claude       *anthropic.Client
	Audit        *AuditWriter
	ApprovalCh   <-chan bool
	StateMachine *RunStateMachine
	// MessagesOverride replaces the Claude API client for testing.
	// When non-nil, Claude is ignored for message calls.
	// TODO(#78): remove once a transport-level Anthropic fake is in place.
	MessagesOverride MessagesAPI
}

// New returns a BoundAgent ready to run, or an error if schema narrowing fails
// for any of the provided tools.
func New(cfg Config) (*BoundAgent, error) {
	if cfg.StateMachine == nil {
		return nil, fmt.Errorf("config.StateMachine is required")
	}

	var msgs MessagesAPI
	if cfg.MessagesOverride != nil {
		msgs = cfg.MessagesOverride
	} else {
		msgs = &cfg.Claude.Messages
	}

	toolsByName := make(map[string]resolvedToolEntry, len(cfg.Tools))
	claudeNameToInternal := make(map[string]string, len(cfg.Tools))
	for _, rt := range cfg.Tools {
		dotName := rt.ServerName + "." + rt.ToolName
		claudeName := sanitizeToolName(dotName)

		// Detect collisions: two distinct tools that sanitize to the same name.
		if existing, conflict := claudeNameToInternal[claudeName]; conflict && existing != dotName {
			return nil, fmt.Errorf("tool name collision after sanitization: %q and %q both become %q", existing, dotName, claudeName)
		}

		narrowed, err := mcp.NarrowSchema(rt.InputSchema, rt.Params)
		if err != nil {
			return nil, fmt.Errorf("narrowing schema for tool %s.%s: %w", rt.ServerName, rt.ToolName, err)
		}
		toolsByName[dotName] = resolvedToolEntry{
			tool:           rt,
			narrowedSchema: narrowed,
		}
		claudeNameToInternal[claudeName] = dotName
	}

	return &BoundAgent{
		policy:               cfg.Policy,
		tools:                cfg.Tools,
		toolsByName:          toolsByName,
		claudeNameToInternal: claudeNameToInternal,
		messages:             msgs,
		audit:                cfg.Audit,
		sm:                   cfg.StateMachine,
		approvalCh:           cfg.ApprovalCh,
	}, nil
}

// failRun transitions the run to failed status and returns the original error.
// If the context is already cancelled, a background context is used so the DB
// write still lands.
func (a *BoundAgent) failRun(ctx context.Context, runErr error) error {
	transCtx := ctx
	if ctx.Err() != nil {
		transCtx = context.Background()
	}
	if tErr := a.sm.Transition(transCtx, model.RunStatusFailed, runErr.Error()); tErr != nil {
		slog.ErrorContext(transCtx, "failed to persist run failure status",
			"run_err", runErr, "transition_err", tErr)
	}
	return runErr
}

// logAuditError writes an error step to the audit trail with the given message and code.
// Callers that pass context.Background() do so intentionally — DB writes must complete
// even after the caller's context is cancelled (e.g. cancellation-path error steps).
func (a *BoundAgent) logAuditError(ctx context.Context, runID string, msg string, code string) {
	if err := a.audit.Write(ctx, Step{
		RunID:   runID,
		Type:    model.StepTypeError,
		Content: map[string]string{"message": msg, "code": code},
	}); err != nil {
		slog.WarnContext(ctx, "audit write failed", "run_id", runID, "err", err)
	}
}

// logTransitionError wraps a failRun call that intentionally discards errors from
// state transitions that fail after the run itself has already failed. Logs via
// slog when the transition itself errors so the failure is not silently swallowed.
func (a *BoundAgent) logTransitionError(ctx context.Context, runErr error) {
	if tErr := a.failRun(ctx, runErr); tErr != nil && tErr != runErr {
		slog.ErrorContext(ctx, "run transition failed", "err", tErr)
	}
}

// checkCapabilities verifies every capability reference in the policy resolves
// to a tool registered at BoundAgent construction time. Called at the start of
// Run(), before the pending→running transition, so a run with unresolvable
// capabilities fails immediately without ever appearing as running.
func (a *BoundAgent) checkCapabilities() error {
	// Collect all referenced tool names from all three capability categories.
	var toolNames []string
	for _, s := range a.policy.Capabilities.Sensors {
		toolNames = append(toolNames, s.Tool)
	}
	for _, act := range a.policy.Capabilities.Actuators {
		toolNames = append(toolNames, act.Tool)
	}
	toolNames = append(toolNames, a.policy.Capabilities.Feedback...)

	for _, name := range toolNames {
		if _, ok := a.toolsByName[name]; !ok {
			return fmt.Errorf("capability '%s' not found in MCP registry — verify the MCP server is registered and the tool exists", name)
		}
	}
	return nil
}

// buildToolDefinitions builds the Anthropic tool definitions slice from the
// agent's registered tools. The claudeNameToInternal mapping is already
// populated in New(), so it is not returned here.
func (a *BoundAgent) buildToolDefinitions() ([]anthropic.ToolUnionParam, error) {
	anthropicTools := make([]anthropic.ToolUnionParam, 0, len(a.toolsByName))

	for dotName, entry := range a.toolsByName {
		inputSchema, err := buildToolInputSchema(entry.narrowedSchema)
		if err != nil {
			return nil, fmt.Errorf("building tool schema for %s: %w", dotName, err)
		}
		claudeName := sanitizeToolName(dotName)

		tool := anthropic.ToolUnionParamOfTool(inputSchema, claudeName)
		// Set description via the OfTool variant directly.
		if tool.OfTool != nil {
			tool.OfTool.Description = param.NewOpt(entry.tool.Description)
		}
		anthropicTools = append(anthropicTools, tool)
	}

	return anthropicTools, nil
}

// waitForApproval suspends the run at an approval gate for the given tool entry.
// It writes the approval_request audit step, then blocks on the approval channel,
// a timeout, or context cancellation.
// Returns nil if approved (or timed out with on_timeout=approve), error otherwise.
func (a *BoundAgent) waitForApproval(ctx context.Context, runID string, entry resolvedToolEntry, internalName string, input map[string]any) error {
	if err := a.audit.Write(ctx, Step{
		RunID:   runID,
		Type:    model.StepTypeApprovalRequest,
		Content: map[string]any{"tool": internalName, "input": input},
	}); err != nil {
		return fmt.Errorf("writing approval request step: %w", err)
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
	case approved := <-a.approvalCh:
		if !approved {
			err := fmt.Errorf("tool call %s rejected by operator", internalName)
			a.logAuditError(ctx, runID, err.Error(), "approval_rejected")
			return err
		}
	case <-timeoutCh:
		slog.WarnContext(ctx, "approval timeout reached",
			"run_id", runID, "tool", internalName,
			"timeout", entry.tool.Timeout.String())
		if entry.tool.OnTimeout == model.OnTimeoutApprove {
			// Proceed with execution on timeout.
		} else {
			err := fmt.Errorf("approval timeout for tool %s", internalName)
			a.logAuditError(ctx, runID, err.Error(), "approval_rejected")
			return err
		}
	case <-ctx.Done():
		return fmt.Errorf("context cancelled waiting for approval: %w", ctx.Err())
	}

	return nil
}

// processContentBlocks iterates the content blocks of a single API response,
// writing audit steps and dispatching tool calls. Returns the tool result
// blocks to feed back to the next API call, the updated totalToolCalls count,
// and any fatal error.
func (a *BoundAgent) processContentBlocks(
	ctx context.Context,
	runID string,
	resp *anthropic.Message,
	totalToolCalls int,
	maxToolCalls int,
	tokenCost int,
) ([]anthropic.ContentBlockParamUnion, int, error) {
	// costAssigned tracks whether the per-turn token cost has been
	// attributed to an audit step yet. We assign it to the first block of
	// any type (text or tool_use) so that tool-use-only turns don't silently
	// drop the cost from the audit trail.
	costAssigned := false

	var toolResults []anthropic.ContentBlockParamUnion

	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			cost := 0
			if !costAssigned {
				cost = tokenCost
				costAssigned = true
			}
			if err := a.audit.Write(ctx, Step{
				RunID:     runID,
				Type:      model.StepTypeThought,
				Content:   map[string]string{"text": b.Text},
				TokenCost: cost,
			}); err != nil {
				return nil, totalToolCalls, fmt.Errorf("writing thought step: %w", err)
			}
		case anthropic.ToolUseBlock:
			totalToolCalls++
			if maxToolCalls > 0 && totalToolCalls > maxToolCalls {
				err := fmt.Errorf("tool call limit exceeded: %d calls, limit %d", totalToolCalls, maxToolCalls)
				slog.WarnContext(ctx, "tool call limit exceeded", "run_id", runID, "calls", totalToolCalls, "limit", maxToolCalls)
				a.logAuditError(ctx, runID, err.Error(), "tool_call_limit_exceeded")
				return nil, totalToolCalls, err
			}

			var input map[string]any
			if err := json.Unmarshal(b.Input, &input); err != nil {
				return nil, totalToolCalls, fmt.Errorf("unmarshalling tool input for %s: %w", b.Name, err)
			}

			resultStr, isError, err := a.handleToolCall(ctx, runID, b.ID, b.Name, input)
			if err != nil {
				return nil, totalToolCalls, err // handleToolCall already writes the error step
			}
			toolResults = append(toolResults, anthropic.NewToolResultBlock(b.ID, resultStr, isError))
		}
	}

	return toolResults, totalToolCalls, nil
}

// runAPILoop drives the Claude API loop until the model returns end_turn,
// a limit is exceeded, or an error occurs. It owns the token and tool-call
// counters for the run.
func (a *BoundAgent) runAPILoop(
	ctx context.Context,
	runID string,
	history []anthropic.MessageParam,
	anthropicTools []anthropic.ToolUnionParam,
	systemPrompt string,
) error {
	var (
		totalTokens    int
		totalToolCalls int
	)

	maxTokensPerRun := a.policy.Agent.Limits.MaxTokensPerRun
	maxToolCalls := a.policy.Agent.Limits.MaxToolCallsPerRun

	for {
		// Respect context cancellation before each API call.
		// context.Background() is used intentionally: the caller's context is
		// already cancelled, so writing with ctx would silently drop the step.
		// These DB writes MUST succeed regardless of caller cancellation to preserve
		// audit trail completeness and final state persistence.
		if err := ctx.Err(); err != nil {
			a.logAuditError(context.Background(), runID, "run cancelled", "cancelled")
			return a.failRun(ctx, fmt.Errorf("agent run cancelled: %w", err))
		}

		// Determine per-call token limit.
		maxTokens := int64(config.DefaultPerCallMaxTokens)
		if maxTokensPerRun > 0 {
			remaining := int64(maxTokensPerRun - totalTokens)
			if remaining <= 0 {
				err := fmt.Errorf("token budget exceeded: %d tokens used, limit %d", totalTokens, maxTokensPerRun)
				slog.WarnContext(ctx, "token budget exceeded", "run_id", runID, "tokens_used", totalTokens, "limit", maxTokensPerRun)
				a.logAuditError(ctx, runID, err.Error(), "token_budget_exceeded")
				return a.failRun(ctx, err)
			}
			if remaining < maxTokens {
				maxTokens = remaining
			}
		}

		resp, err := a.messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(a.policy.Agent.Model),
			MaxTokens: maxTokens,
			System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
			Messages:  history,
			Tools:     anthropicTools,
		})
		if err != nil {
			// If the context was cancelled, the API error is a consequence of
			// cancellation. Write a CANCELLED step so the audit trail is clear.
			// context.Background() is used in all cases — ctx may already be done.
			if ctx.Err() != nil {
				a.logAuditError(context.Background(), runID, "run cancelled", "cancelled")
			} else {
				a.logAuditError(context.Background(), runID, err.Error(), "api_error")
			}
			return a.failRun(ctx, fmt.Errorf("claude API call: %w", err))
		}

		tokenCost := int(resp.Usage.InputTokens + resp.Usage.OutputTokens)
		totalTokens += tokenCost

		toolResults, updatedToolCalls, err := a.processContentBlocks(ctx, runID, resp, totalToolCalls, maxToolCalls, tokenCost)
		totalToolCalls = updatedToolCalls
		if err != nil {
			return a.failRun(ctx, err)
		}

		// Append assistant response to history before checking stop reason.
		history = append(history, resp.ToParam())

		if resp.StopReason == anthropic.StopReasonEndTurn {
			if err := a.audit.Write(ctx, Step{
				RunID:   runID,
				Type:    model.StepTypeComplete,
				Content: map[string]string{"message": "agent completed task"},
			}); err != nil {
				return a.failRun(ctx, fmt.Errorf("writing complete step: %w", err))
			}
			if err := a.sm.Transition(ctx, model.RunStatusComplete, ""); err != nil {
				return fmt.Errorf("transitioning run to complete: %w", err)
			}
			return nil
		}

		// If stop reason is tool_use but no tool calls were dispatched, the
		// API response is malformed (e.g. unknown block types). Sending
		// the assistant message back with no tool results would violate the
		// protocol and likely cause the next API call to fail.
		if len(toolResults) == 0 {
			return a.failRun(ctx, fmt.Errorf("tool_use stop reason with no tool calls dispatched"))
		}

		// Prepend a current-time text block so the agent is aware of elapsed
		// time between tool calls (issue #205). The system prompt carries the
		// static run-start timestamp; this per-turn timestamp is the clock.
		timeBlock := anthropic.NewTextBlock(
			fmt.Sprintf("[Current time: %s]", time.Now().UTC().Format(config.TimestampFormat)),
		)
		toolResults = append([]anthropic.ContentBlockParamUnion{timeBlock}, toolResults...)
		history = append(history, anthropic.NewUserMessage(toolResults...))
	}
}

// Run executes the agent loop for a single run. It drives the Claude API
// until the model produces end_turn or the run limits are exceeded.
// Run returns nil on clean completion, or a wrapped error on failure.
// Run owns the run's status transitions: it moves the run to running on entry,
// complete on success, and failed on any error path.
func (a *BoundAgent) Run(ctx context.Context, runID string, triggerPayload string) error {
	// Run owns the AuditWriter lifecycle. Close is idempotent, so callers that
	// already held a reference to the writer can still call Close safely.
	defer func() {
		if err := a.audit.Close(); err != nil {
			slog.ErrorContext(ctx, "audit writer drain error", "run_id", runID, "err", err)
		}
	}()

	// Fail fast before entering running state: every capability referenced by the
	// policy must resolve to a registered tool. Checked here (pending→failed) so the
	// run never briefly appears running when it has no chance of succeeding.
	if err := a.checkCapabilities(); err != nil {
		a.logAuditError(ctx, runID, err.Error(), "missing_capability")
		return a.failRun(ctx, err)
	}

	// Transition to running only after pre-flight checks pass. Use
	// context.Background() so the DB write lands even if the caller's context
	// is already cancelled — the loop will detect cancellation immediately.
	if err := a.sm.Transition(context.Background(), model.RunStatusRunning, ""); err != nil {
		// Best-effort: attempt to mark the run failed. If this also fails, log and
		// return the original transition error — the run will be cleaned up by the
		// startup scan on next restart.
		a.logTransitionError(context.Background(), err)
		return fmt.Errorf("transitioning run to running: %w", err)
	}

	// Extract granted tools for system prompt rendering.
	grantedTools := make([]model.GrantedTool, len(a.tools))
	for i, rt := range a.tools {
		grantedTools[i] = rt.GrantedTool
	}

	// Render system prompt (ADR-001: only granted tools are visible to the agent).
	systemPrompt := policy.RenderSystemPrompt(a.policy, grantedTools, time.Now().UTC())

	if err := a.sm.PersistSystemPrompt(ctx, systemPrompt); err != nil {
		slog.WarnContext(ctx, "failed to persist system prompt", "run_id", runID, "err", err)
	}

	anthropicTools, err := a.buildToolDefinitions()
	if err != nil {
		return a.failRun(ctx, err)
	}

	// capabilitySnapshot is the content written to the capability_snapshot step (ADR-018).
	// Including the model alongside tools makes the snapshot a complete record of
	// the agent's configuration at run start.
	type capabilitySnapshot struct {
		Model string              `json:"model"`
		Tools []model.GrantedTool `json:"tools"`
	}

	// Write capability snapshot step (ADR-018) — always the first step.
	// Use context.Background() so this initialization step always lands, even if
	// the caller's context was already cancelled before Run was entered.
	if err := a.audit.Write(context.Background(), Step{
		RunID: runID,
		Type:  model.StepTypeCapabilitySnapshot,
		Content: capabilitySnapshot{
			Model: a.policy.Agent.Model,
			Tools: grantedTools,
		},
	}); err != nil {
		return a.failRun(ctx, fmt.Errorf("writing capability snapshot: %w", err))
	}

	// Initialize message history with the trigger payload.
	history := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(triggerPayload)),
	}

	return a.runAPILoop(ctx, runID, history, anthropicTools, systemPrompt)
}

// handleToolCall dispatches a single tool call from the agent.
// For approval-gated actuators it suspends the run and waits for a decision
// before proceeding. This is the hard runtime guarantee (ADR-001).
// On error, it writes an error step and returns the error.
//
// toolName is the sanitized Claude-facing name returned by the API. It is
// resolved to the internal dot-separated name via claudeNameToInternal before
// any audit writes or tool lookups.
func (a *BoundAgent) handleToolCall(ctx context.Context, runID, _ /*toolUseID*/, toolName string, input map[string]any) (string, bool, error) {
	// Bail early if the context is already cancelled — no steps should be
	// written and no MCP call should be made.
	if err := ctx.Err(); err != nil {
		return "", false, fmt.Errorf("context cancelled before tool dispatch: %w", err)
	}

	// Resolve the sanitized Claude-facing name to the internal dot-separated name.
	internalName, ok := a.claudeNameToInternal[toolName]
	if !ok {
		// Fall back to the raw name for the error message so callers can see the
		// exact name Claude returned.
		internalName = toolName
	}

	entry, ok := a.toolsByName[internalName]
	if !ok {
		err := fmt.Errorf("tool not found: %s", toolName)
		a.logAuditError(ctx, runID, err.Error(), "tool_error")
		return "", false, err
	}

	// Validate input against narrowed schema.
	if err := mcp.ValidateCall(entry.narrowedSchema, input); err != nil {
		a.logAuditError(ctx, runID, err.Error(), "schema_violation")
		return "", false, fmt.Errorf("schema validation for %s: %w", internalName, err)
	}

	// Approval gating for actuators with approval: required.
	if entry.tool.Role == model.CapabilityRoleActuator && entry.tool.Approval == model.ApprovalModeRequired {
		if err := a.waitForApproval(ctx, runID, entry, internalName, input); err != nil {
			return "", false, err
		}
	}

	// Write tool_call step.
	if err := a.audit.Write(ctx, Step{
		RunID: runID,
		Type:  model.StepTypeToolCall,
		Content: map[string]any{
			"tool_name": internalName,
			"server_id": entry.tool.ServerName,
			"input":     input,
		},
	}); err != nil {
		return "", false, fmt.Errorf("writing tool_call step: %w", err)
	}

	// Dispatch to MCP server.
	result, err := entry.tool.Client.CallTool(ctx, entry.tool.ToolName, input)
	if err != nil {
		// If the context was cancelled, write a canonical CANCELLED step rather
		// than a tool_error so all cancellation paths produce consistent audit output.
		// context.Background() is used because ctx may already be done.
		if ctx.Err() != nil {
			a.logAuditError(context.Background(), runID, "run cancelled", "cancelled")
		} else {
			a.logAuditError(context.Background(), runID, err.Error(), "tool_error")
		}
		return "", false, fmt.Errorf("calling tool %s: %w", internalName, err)
	}

	outputStr := string(result.Output)

	// Write tool_result step.
	if err := a.audit.Write(ctx, Step{
		RunID: runID,
		Type:  model.StepTypeToolResult,
		Content: map[string]any{
			"tool_name": internalName,
			"output":    outputStr,
			"is_error":  result.IsError,
		},
	}); err != nil {
		return "", false, fmt.Errorf("writing tool_result step: %w", err)
	}

	return outputStr, result.IsError, nil
}

// buildToolInputSchema converts a raw JSON schema into a ToolInputSchemaParam
// for use in the Anthropic API.
func buildToolInputSchema(schema json.RawMessage) (anthropic.ToolInputSchemaParam, error) {
	if len(schema) == 0 {
		return anthropic.ToolInputSchemaParam{}, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(schema, &raw); err != nil {
		return anthropic.ToolInputSchemaParam{}, fmt.Errorf("unmarshal schema: %w", err)
	}

	var properties any
	if props, ok := raw["properties"]; ok {
		properties = props
	}

	var required []string
	if req, ok := raw["required"]; ok {
		if reqSlice, ok := req.([]any); ok {
			for _, v := range reqSlice {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		}
	}

	// Copy any extra fields (e.g. "additionalProperties", "$schema") so the
	// schema round-trips cleanly.
	extras := make(map[string]any)
	for k, v := range raw {
		if k != "type" && k != "properties" && k != "required" {
			extras[k] = v
		}
	}

	return anthropic.ToolInputSchemaParam{
		Properties:  properties,
		Required:    required,
		ExtraFields: extras,
	}, nil
}

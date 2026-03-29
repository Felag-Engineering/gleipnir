// Package agent implements the BoundAgent runner — the core LLM API loop
// with hard capability enforcement, approval interception, and audit writing.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// resolvedToolEntry holds a ResolvedTool paired with its narrowed JSON schema.
type resolvedToolEntry struct {
	tool           mcp.ResolvedTool
	narrowedSchema json.RawMessage
}

// BoundAgent executes a single policy run. It owns the LLM API loop,
// dispatches tool calls to MCP clients, intercepts approval-gated tools,
// and writes every step to the audit trail via AuditWriter.
type BoundAgent struct {
	policy      *model.ParsedPolicy
	tools       []mcp.ResolvedTool
	toolsByName map[string]resolvedToolEntry
	llmClient   llm.LLMClient
	audit       *AuditWriter
	sm          *RunStateMachine
	// approvalCh receives the operator's decision when a run is suspended
	// waiting_for_approval. Sent by the approval handler in internal/trigger.
	approvalCh <-chan bool
}

// Config holds the dependencies needed to construct a BoundAgent.
type Config struct {
	Policy       *model.ParsedPolicy
	Tools        []mcp.ResolvedTool
	LLMClient    llm.LLMClient
	Audit        *AuditWriter
	ApprovalCh   <-chan bool
	StateMachine *RunStateMachine
}

// New returns a BoundAgent ready to run, or an error if schema narrowing fails
// for any of the provided tools.
func New(cfg Config) (*BoundAgent, error) {
	if cfg.StateMachine == nil {
		return nil, fmt.Errorf("config.StateMachine is required")
	}

	toolsByName := make(map[string]resolvedToolEntry, len(cfg.Tools))
	for _, rt := range cfg.Tools {
		dotName := rt.ServerName + "." + rt.ToolName

		narrowed, err := mcp.NarrowSchema(rt.InputSchema, rt.Params)
		if err != nil {
			return nil, fmt.Errorf("narrowing schema for tool %s.%s: %w", rt.ServerName, rt.ToolName, err)
		}
		toolsByName[dotName] = resolvedToolEntry{
			tool:           rt,
			narrowedSchema: narrowed,
		}
	}

	return &BoundAgent{
		policy:      cfg.Policy,
		tools:       cfg.Tools,
		toolsByName: toolsByName,
		llmClient:   cfg.LLMClient,
		audit:       cfg.Audit,
		sm:          cfg.StateMachine,
		approvalCh:  cfg.ApprovalCh,
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
func (a *BoundAgent) logAuditError(ctx context.Context, runID string, msg string, code model.ErrorCode) {
	if err := a.audit.Write(ctx, Step{
		RunID:   runID,
		Type:    model.StepTypeError,
		Content: model.ErrorStepContent{Message: msg, Code: code},
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
	// Collect all referenced tool names from both capability categories.
	var toolNames []string
	for _, t := range a.policy.Capabilities.Tools {
		toolNames = append(toolNames, t.Tool)
	}
	toolNames = append(toolNames, a.policy.Capabilities.Feedback...)

	for _, name := range toolNames {
		if _, ok := a.toolsByName[name]; !ok {
			return fmt.Errorf("capability '%s' not found in MCP registry — verify the MCP server is registered and the tool exists", name)
		}
	}
	return nil
}

// buildToolDefinitions builds the provider-neutral tool definitions from the
// agent's registered tools. The LLMClient handles provider-specific name
// sanitization and schema formatting.
func (a *BoundAgent) buildToolDefinitions() []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(a.toolsByName))
	for dotName, entry := range a.toolsByName {
		defs = append(defs, llm.ToolDefinition{
			Name:        dotName,
			Description: entry.tool.Description,
			InputSchema: entry.narrowedSchema,
		})
	}
	return defs
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
			a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeApprovalRejected)
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
			a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeApprovalRejected)
			return err
		}
	case <-ctx.Done():
		return fmt.Errorf("context cancelled waiting for approval: %w", ctx.Err())
	}

	return nil
}

// runAPILoop drives the LLM API loop until the model returns end_turn,
// a limit is exceeded, or an error occurs. It owns the token and tool-call
// counters for the run.
func (a *BoundAgent) runAPILoop(
	ctx context.Context,
	runID string,
	history []llm.ConversationTurn,
	tools []llm.ToolDefinition,
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
			a.logAuditError(context.Background(), runID, "run cancelled", model.ErrorCodeCancelled)
			return a.failRun(ctx, fmt.Errorf("agent run cancelled: %w", err))
		}

		// Determine per-call token limit.
		maxTokens := config.DefaultPerCallMaxTokens
		if maxTokensPerRun > 0 {
			remaining := maxTokensPerRun - totalTokens
			if remaining <= 0 {
				err := fmt.Errorf("token budget exceeded: %d tokens used, limit %d", totalTokens, maxTokensPerRun)
				slog.WarnContext(ctx, "token budget exceeded", "run_id", runID, "tokens_used", totalTokens, "limit", maxTokensPerRun)
				a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeTokenBudgetExceeded)
				return a.failRun(ctx, err)
			}
			if remaining < maxTokens {
				maxTokens = remaining
			}
		}

		req := llm.MessageRequest{
			Model:        a.policy.Agent.ModelConfig.Name,
			MaxTokens:    maxTokens,
			SystemPrompt: systemPrompt,
			History:      history,
			Tools:        tools,
		}

		resp, err := a.llmClient.CreateMessage(ctx, req)
		if err != nil {
			// If the context was cancelled, the API error is a consequence of
			// cancellation. Write a CANCELLED step so the audit trail is clear.
			// context.Background() is used in all cases — ctx may already be done.
			if ctx.Err() != nil {
				a.logAuditError(context.Background(), runID, "run cancelled", model.ErrorCodeCancelled)
			} else {
				a.logAuditError(context.Background(), runID, err.Error(), model.ErrorCodeAPIError)
			}
			return a.failRun(ctx, fmt.Errorf("LLM API call: %w", err))
		}

		tokenCost := resp.Usage.InputTokens + resp.Usage.OutputTokens
		totalTokens += tokenCost

		// costAssigned tracks whether the per-turn token cost has been attributed
		// to an audit step yet. We assign it to the first text block so that
		// tool-use-only turns don't silently drop the cost.
		costAssigned := false
		var toolResultBlocks []llm.ContentBlock

		// Process thinking blocks: write thinking audit steps before text blocks
		// so the first thinking block carries the token cost on thinking-heavy turns.
		for _, tb := range resp.Thinking {
			cost := 0
			if !costAssigned {
				cost = tokenCost
				costAssigned = true
			}
			if err := a.audit.Write(ctx, Step{
				RunID:     runID,
				Type:      model.StepTypeThinking,
				Content:   map[string]any{"text": tb.Text, "redacted": tb.Redacted},
				TokenCost: cost,
			}); err != nil {
				return a.failRun(ctx, fmt.Errorf("writing thinking step: %w", err))
			}
		}

		// Process text blocks: write thought audit steps.
		for _, tb := range resp.Text {
			cost := 0
			if !costAssigned {
				cost = tokenCost
				costAssigned = true
			}
			if err := a.audit.Write(ctx, Step{
				RunID:     runID,
				Type:      model.StepTypeThought,
				Content:   map[string]string{"text": tb.Text},
				TokenCost: cost,
			}); err != nil {
				return a.failRun(ctx, fmt.Errorf("writing thought step: %w", err))
			}
		}

		// Process tool calls: dispatch and collect results.
		for _, tc := range resp.ToolCalls {
			if !costAssigned {
				// Assign cost to the first tool call block when there are no text blocks.
				costAssigned = true
				// Cost was already added to totalTokens; the audit step for tool_call
				// will carry zero cost. The token cost is reflected in the total only.
				// (The thought audit step is the canonical bearer of token cost.)
			}

			totalToolCalls++
			if maxToolCalls > 0 && totalToolCalls > maxToolCalls {
				err := fmt.Errorf("tool call limit exceeded: %d calls, limit %d", totalToolCalls, maxToolCalls)
				slog.WarnContext(ctx, "tool call limit exceeded", "run_id", runID, "calls", totalToolCalls, "limit", maxToolCalls)
				a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeToolCallLimitExceeded)
				return a.failRun(ctx, err)
			}

			var input map[string]any
			if err := json.Unmarshal(tc.Input, &input); err != nil {
				return a.failRun(ctx, fmt.Errorf("unmarshalling tool input for %s: %w", tc.Name, err))
			}

			resultStr, isError, err := a.handleToolCall(ctx, runID, tc.Name, input)
			if err != nil {
				return a.failRun(ctx, err)
			}
			toolResultBlocks = append(toolResultBlocks, llm.ToolResultBlock{
				ToolCallID: tc.ID,
				Content:    resultStr,
				IsError:    isError,
			})
		}

		// Append assistant turn to history (reconstruct from resp fields).
		assistantContent := make([]llm.ContentBlock, 0, len(resp.Text)+len(resp.ToolCalls))
		for _, tb := range resp.Text {
			assistantContent = append(assistantContent, tb)
		}
		for _, tc := range resp.ToolCalls {
			assistantContent = append(assistantContent, tc)
		}
		history = append(history, llm.ConversationTurn{
			Role:    llm.RoleAssistant,
			Content: assistantContent,
		})

		if resp.StopReason == llm.StopReasonEndTurn {
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

		// MaxTokens stop reason: the model was truncated. Fail the run so the
		// operator knows the token budget was too tight. (New behavior: previously
		// unhandled — see issue #340 / ADR-026.)
		if resp.StopReason == llm.StopReasonMaxTokens {
			err := fmt.Errorf("LLM response truncated: max_tokens limit reached")
			a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeTokenBudgetExceeded)
			return a.failRun(ctx, err)
		}

		// If stop reason is tool_use but no tool calls were dispatched, the
		// API response is malformed (e.g. unknown block types). Sending
		// the assistant message back with no tool results would violate the
		// protocol and likely cause the next API call to fail.
		if len(toolResultBlocks) == 0 {
			return a.failRun(ctx, fmt.Errorf("tool_use stop reason with no tool calls dispatched"))
		}

		// Prepend a current-time text block so the agent is aware of elapsed
		// time between tool calls (issue #205). The system prompt carries the
		// static run-start timestamp; this per-turn timestamp is the clock.
		timeBlock := llm.TextBlock{
			Text: fmt.Sprintf("[Current time: %s]", time.Now().UTC().Format(config.TimestampFormat)),
		}
		userContent := make([]llm.ContentBlock, 0, 1+len(toolResultBlocks))
		userContent = append(userContent, timeBlock)
		userContent = append(userContent, toolResultBlocks...)
		history = append(history, llm.ConversationTurn{
			Role:    llm.RoleUser,
			Content: userContent,
		})
	}
}

// Run executes the agent loop for a single run. It drives the LLM API
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
		a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeMissingCapability)
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

	tools := a.buildToolDefinitions()

	// capabilitySnapshot is the content written to the capability_snapshot step (ADR-018).
	// Including provider and model alongside tools makes the snapshot a complete record of
	// the agent's configuration at run start.
	type capabilitySnapshot struct {
		Provider string              `json:"provider"`
		Model    string              `json:"model"`
		Tools    []model.GrantedTool `json:"tools"`
	}

	// Write capability snapshot step (ADR-018) — always the first step.
	// Use context.Background() so this initialization step always lands, even if
	// the caller's context was already cancelled before Run was entered.
	if err := a.audit.Write(context.Background(), Step{
		RunID: runID,
		Type:  model.StepTypeCapabilitySnapshot,
		Content: capabilitySnapshot{
			Provider: a.policy.Agent.ModelConfig.Provider,
			Model:    a.policy.Agent.ModelConfig.Name,
			Tools:    grantedTools,
		},
	}); err != nil {
		return a.failRun(ctx, fmt.Errorf("writing capability snapshot: %w", err))
	}

	// Initialize conversation history with the trigger payload as the first user turn.
	history := []llm.ConversationTurn{{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{llm.TextBlock{Text: triggerPayload}},
	}}

	return a.runAPILoop(ctx, runID, history, tools, systemPrompt)
}

// handleToolCall dispatches a single tool call from the agent.
// For approval-gated tools it suspends the run and waits for a decision
// before proceeding. This is the hard runtime guarantee (ADR-001).
// On error, it writes an error step and returns the error.
//
// toolName is the original MCP dot-separated name (e.g. "myserver.read_data"),
// reverse-mapped by the LLMClient before being passed here.
func (a *BoundAgent) handleToolCall(ctx context.Context, runID, toolName string, input map[string]any) (string, bool, error) {
	// Bail early if the context is already cancelled — no steps should be
	// written and no MCP call should be made.
	if err := ctx.Err(); err != nil {
		return "", false, fmt.Errorf("context cancelled before tool dispatch: %w", err)
	}

	entry, ok := a.toolsByName[toolName]
	if !ok {
		err := fmt.Errorf("tool not found: %s", toolName)
		a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeToolError)
		return "", false, err
	}

	// Validate input against narrowed schema.
	if err := mcp.ValidateCall(entry.narrowedSchema, input); err != nil {
		a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeSchemaViolation)
		return "", false, fmt.Errorf("schema validation for %s: %w", toolName, err)
	}

	// Approval gating for tools with approval: required.
	if entry.tool.Approval == model.ApprovalModeRequired {
		if err := a.waitForApproval(ctx, runID, entry, toolName, input); err != nil {
			return "", false, err
		}
	}

	// Write tool_call step.
	if err := a.audit.Write(ctx, Step{
		RunID: runID,
		Type:  model.StepTypeToolCall,
		Content: map[string]any{
			"tool_name": toolName,
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
			a.logAuditError(context.Background(), runID, "run cancelled", model.ErrorCodeCancelled)
		} else {
			a.logAuditError(context.Background(), runID, err.Error(), model.ErrorCodeToolError)
		}
		return "", false, fmt.Errorf("calling tool %s: %w", toolName, err)
	}

	outputStr := string(result.Output)

	// Write tool_result step.
	if err := a.audit.Write(ctx, Step{
		RunID: runID,
		Type:  model.StepTypeToolResult,
		Content: map[string]any{
			"tool_name": toolName,
			"output":    outputStr,
			"is_error":  result.IsError,
		},
	}); err != nil {
		return "", false, fmt.Errorf("writing tool_result step: %w", err)
	}

	return outputStr, result.IsError, nil
}

// Package agent implements the BoundAgent runner — the core LLM API loop
// with hard capability enforcement, approval interception, and audit writing.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/logctx"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

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
	approvals   *ApprovalHandler
	feedback    *FeedbackHandler
}

// Config holds the dependencies needed to construct a BoundAgent.
type Config struct {
	Policy                 *model.ParsedPolicy
	Tools                  []mcp.ResolvedTool
	LLMClient              llm.LLMClient
	Audit                  *AuditWriter
	ApprovalCh             <-chan bool
	FeedbackCh             <-chan string
	StateMachine           *RunStateMachine
	DefaultFeedbackTimeout time.Duration
}

// New returns a BoundAgent ready to run, or an error if schema narrowing fails
// for any of the provided tools.
func New(cfg Config) (*BoundAgent, error) {
	if cfg.StateMachine == nil {
		return nil, fmt.Errorf("config.StateMachine is required")
	}

	toolsByName, err := buildResolvedToolMap(cfg.Tools)
	if err != nil {
		return nil, err
	}

	return &BoundAgent{
		policy:      cfg.Policy,
		tools:       cfg.Tools,
		toolsByName: toolsByName,
		llmClient:   cfg.LLMClient,
		audit:       cfg.Audit,
		sm:          cfg.StateMachine,
		approvals:   NewApprovalHandler(cfg.Audit, cfg.StateMachine, cfg.ApprovalCh),
		feedback:    NewFeedbackHandler(cfg.Audit, cfg.StateMachine, cfg.FeedbackCh, cfg.DefaultFeedbackTimeout),
	}, nil
}

// failRun delegates to the package-level free function in errors.go.
func (a *BoundAgent) failRun(ctx context.Context, runErr error) error {
	return failRun(ctx, a.sm, runErr)
}

// logAuditError writes an error step to the audit trail with the given message and code.
// Safe to call with a cancelled context — the underlying function swaps to a
// background context when needed.
//
// Test seam — delegates to package-level logAuditError in audit.go.
// Safe to inline the free function call once agent_test.go migrates away from this method.
func (a *BoundAgent) logAuditError(ctx context.Context, runID string, msg string, code model.ErrorCode) {
	logAuditError(ctx, a.audit, Step{
		RunID:   runID,
		Type:    model.StepTypeError,
		Content: model.ErrorStepContent{Message: msg, Code: code},
	})
}

// logTransitionError delegates to the package-level free function in errors.go.
func (a *BoundAgent) logTransitionError(ctx context.Context, runErr error) {
	logTransitionError(ctx, a.sm, runErr)
}

// waitForApproval delegates to the ApprovalHandler.
//
// Test seam — delegates to handler. Safe to delete once agent_test.go migrates
// to handler-level tests.
func (a *BoundAgent) waitForApproval(ctx context.Context, runID string, entry resolvedToolEntry, internalName string, input map[string]any) error {
	return a.approvals.Wait(ctx, runID, entry, internalName, input)
}

// waitForFeedback delegates to the FeedbackHandler.
//
// Test seam — delegates to handler. Safe to delete once agent_test.go migrates
// to handler-level tests.
func (a *BoundAgent) waitForFeedback(ctx context.Context, runID, toolName, inputJSON, mcpOutput string, feedbackTimeout time.Duration) (string, error) {
	return a.feedback.Wait(ctx, runID, toolName, inputJSON, mcpOutput, feedbackTimeout)
}

// assignTokenCost computes per-step token cost allocation for a single LLM turn.
// The full turn cost is assigned to the first available step in priority order:
// thinking blocks first, then text blocks. Tool call steps never carry token cost —
// the thought or thinking step is the canonical bearer of the per-turn cost.
// For tool-only turns (no thinking or text blocks), cost is tracked in the run
// total but not attributed to any individual step; this is intentional.
func assignTokenCost(cost int, thinkingCount, textCount int) (thinkingCosts, textCosts []int) {
	thinkingCosts = make([]int, thinkingCount)
	textCosts = make([]int, textCount)
	if thinkingCount > 0 {
		thinkingCosts[0] = cost
	} else if textCount > 0 {
		textCosts[0] = cost
	}
	return
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
		if err := ctx.Err(); err != nil {
			a.logAuditError(ctx, runID, "run cancelled", model.ErrorCodeCancelled)
			return a.failRun(ctx, fmt.Errorf("agent run cancelled: %w", err))
		}

		// Determine per-call token limit.
		maxTokens := config.DefaultPerCallMaxTokens
		if maxTokensPerRun > 0 {
			remaining := maxTokensPerRun - totalTokens
			if remaining <= 0 {
				err := fmt.Errorf("token budget exceeded: %d tokens used, limit %d", totalTokens, maxTokensPerRun)
				logctx.Logger(ctx).WarnContext(ctx, "token budget exceeded", "tokens_used", totalTokens, "limit", maxTokensPerRun)
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
			if ctx.Err() != nil {
				a.logAuditError(ctx, runID, "run cancelled", model.ErrorCodeCancelled)
			} else {
				a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeAPIError)
			}
			return a.failRun(ctx, fmt.Errorf("LLM API call: %w", err))
		}

		tokenCost := resp.Usage.InputTokens + resp.Usage.OutputTokens + resp.Usage.ThinkingTokens
		totalTokens += tokenCost

		thinkingCosts, textCosts := assignTokenCost(tokenCost, len(resp.Thinking), len(resp.Text))
		var toolResultBlocks []llm.ContentBlock

		// Process thinking blocks: write thinking audit steps before text blocks
		// so the first thinking block carries the token cost on thinking-heavy turns.
		for i, tb := range resp.Thinking {
			if err := a.audit.Write(ctx, Step{
				RunID:     runID,
				Type:      model.StepTypeThinking,
				Content:   map[string]any{"text": tb.Text, "redacted": tb.Redacted},
				TokenCost: thinkingCosts[i],
			}); err != nil {
				return a.failRun(ctx, fmt.Errorf("writing thinking step: %w", err))
			}
		}

		// Process text blocks: write thought audit steps.
		for i, tb := range resp.Text {
			if err := a.audit.Write(ctx, Step{
				RunID:     runID,
				Type:      model.StepTypeThought,
				Content:   map[string]string{"text": tb.Text},
				TokenCost: textCosts[i],
			}); err != nil {
				return a.failRun(ctx, fmt.Errorf("writing thought step: %w", err))
			}
		}

		// Process tool calls: dispatch and collect results.
		// Tool call steps carry zero token cost — see assignTokenCost.
		for _, tc := range resp.ToolCalls {
			totalToolCalls++
			if maxToolCalls > 0 && totalToolCalls > maxToolCalls {
				err := fmt.Errorf("tool call limit exceeded: %d calls, limit %d", totalToolCalls, maxToolCalls)
				logctx.Logger(ctx).WarnContext(ctx, "tool call limit exceeded", "calls", totalToolCalls, "limit", maxToolCalls)
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

		// Append assistant turn to history. ThinkingBlocks are prepended before
		// text and tool calls because Anthropic's API requires thinking blocks to
		// appear first in the content array.
		assistantContent := make([]llm.ContentBlock, 0, len(resp.Thinking)+len(resp.Text)+len(resp.ToolCalls))
		for _, tb := range resp.Thinking {
			assistantContent = append(assistantContent, tb)
		}
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
		userContent = append(userContent, toolResultBlocks...)
		userContent = append(userContent, timeBlock)
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
			logctx.Logger(ctx).ErrorContext(ctx, "audit writer drain error", "err", err)
		}
	}()

	// Fail fast before entering running state: every capability referenced by the
	// policy must resolve to a registered tool. Checked here (pending→failed) so the
	// run never briefly appears running when it has no chance of succeeding.
	if err := a.checkCapabilities(); err != nil {
		a.logAuditError(ctx, runID, err.Error(), model.ErrorCodeMissingCapability)
		return a.failRun(ctx, err)
	}

	// Transition to running only after pre-flight checks pass.
	if err := a.sm.Transition(ctx, model.RunStatusRunning, ""); err != nil {
		if ctx.Err() != nil {
			a.logAuditError(ctx, runID, "run cancelled", model.ErrorCodeCancelled)
		}
		a.logTransitionError(ctx, err)
		return fmt.Errorf("transitioning run to running: %w", err)
	}

	// Extract granted tools for system prompt rendering.
	grantedTools := make([]model.GrantedTool, len(a.tools))
	for i, rt := range a.tools {
		grantedTools[i] = rt.GrantedTool
	}
	// Include the synthetic ask_operator tool in the capability snapshot when
	// feedback is enabled, so the audit trail reflects the full set of tools
	// available to the agent at run start.
	if a.policy.Capabilities.Feedback.Enabled {
		grantedTools = append(grantedTools, model.GrantedTool{
			ServerName: "gleipnir",
			ToolName:   "ask_operator",
		})
	}

	// Render system prompt (ADR-001: only granted tools are visible to the agent).
	systemPrompt := policy.RenderSystemPrompt(a.policy, grantedTools, time.Now().UTC())

	if err := a.sm.PersistSystemPrompt(ctx, systemPrompt); err != nil {
		logctx.Logger(ctx).WarnContext(ctx, "failed to persist system prompt", "err", err)
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
	if err := a.audit.Write(ctx, Step{
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

	// Route synthetic tools (gleipnir.* namespace) to the feedback handler
	// before attempting any MCP registry lookup. Synthetic tools are never
	// registered in toolsByName and must never be dispatched to an MCP server.
	if strings.HasPrefix(toolName, SyntheticToolPrefix) {
		return a.feedback.HandleAskOperator(ctx, runID, toolName, input, a.policy.Capabilities.Feedback)
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

	// Approval gating for tools with approval: required (ADR-008).
	// This interception must remain BEFORE the tool_call step is written and
	// BEFORE MCP dispatch — it is the hard runtime guarantee.
	if entry.tool.Approval == model.ApprovalModeRequired {
		if err := a.approvals.Wait(ctx, runID, entry, toolName, input); err != nil {
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
		// Context cancellation is fatal — operator intent, don't mask it.
		if ctx.Err() != nil {
			a.logAuditError(ctx, runID, "run cancelled", model.ErrorCodeCancelled)
			return "", false, fmt.Errorf("calling tool %s: %w", toolName, err)
		}

		// Transport/MCP errors become tool_result steps so the agent can reason
		// about the failure instead of the run aborting.
		sanitized := classifyMCPError(entry.tool.ServerName, err)
		logctx.Logger(ctx).ErrorContext(ctx, "MCP tool call failed",
			"tool", toolName,
			"server", entry.tool.ServerName,
			"error", err,
		)
		if writeErr := a.audit.Write(ctx, Step{
			RunID: runID,
			Type:  model.StepTypeToolResult,
			Content: map[string]any{
				"tool_name": toolName,
				"output":    sanitized,
				"is_error":  true,
			},
		}); writeErr != nil {
			return "", false, fmt.Errorf("writing tool_result step: %w", writeErr)
		}
		return sanitized, true, nil
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

// classifyMCPError produces a sanitized, agent-facing error message from a raw
// MCP transport error. The raw error (which may contain internal hostnames, DNS
// resolver addresses, etc.) is kept out of the audit trail and only logged via
// slog for operator debugging.
func classifyMCPError(serverName string, err error) string {
	var httpErr *mcp.HTTPStatusError
	if errors.As(err, &httpErr) {
		return fmt.Sprintf("MCP server %s returned HTTP %d", serverName, httpErr.StatusCode)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Sprintf("MCP server %s DNS resolution failed", serverName)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Sprintf("MCP server %s connection refused", serverName)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("MCP server %s timed out", serverName)
	}
	var rpcErr *mcp.JSONRPCError
	if errors.As(err, &rpcErr) {
		return fmt.Sprintf("MCP server %s returned an error: %s", serverName, rpcErr.Message)
	}
	return fmt.Sprintf("MCP server %s is unavailable", serverName)
}

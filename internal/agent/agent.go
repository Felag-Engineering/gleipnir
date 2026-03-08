// Package agent implements the BoundAgent runner — the core Claude API loop
// with hard capability enforcement, approval interception, and audit writing.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// messagesAPI is the subset of the Anthropic Messages service used by BoundAgent.
// Extracted as an interface for testing — real code uses cfg.Claude.Messages directly.
type messagesAPI interface {
	New(ctx context.Context, body anthropic.MessageNewParams, opts ...option.RequestOption) (*anthropic.Message, error)
}

// resolvedToolEntry holds a ResolvedTool paired with its narrowed JSON schema.
type resolvedToolEntry struct {
	tool           mcp.ResolvedTool
	narrowedSchema json.RawMessage
}

// BoundAgent executes a single policy run. It owns the Claude API loop,
// dispatches tool calls to MCP clients, intercepts approval-gated actuators,
// and writes every step to the audit trail via AuditWriter.
type BoundAgent struct {
	policy      *model.ParsedPolicy
	tools       []mcp.ResolvedTool
	toolsByName map[string]resolvedToolEntry
	messages    messagesAPI
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
	Claude       *anthropic.Client
	Audit        *AuditWriter
	ApprovalCh   <-chan bool
	StateMachine *RunStateMachine
	// MessagesOverride replaces the Claude API client for testing.
	// When non-nil, Claude is ignored for message calls.
	MessagesOverride messagesAPI
}

// New returns a BoundAgent ready to run, or an error if schema narrowing fails
// for any of the provided tools.
func New(cfg Config) (*BoundAgent, error) {
	if cfg.StateMachine == nil {
		return nil, fmt.Errorf("config.StateMachine is required")
	}

	var msgs messagesAPI
	if cfg.MessagesOverride != nil {
		msgs = cfg.MessagesOverride
	} else {
		msgs = &cfg.Claude.Messages
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
		messages:    msgs,
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

// checkCapabilities verifies every capability reference in the policy resolves
// to a tool registered at BoundAgent construction time. Called at the start of
// Run(), before the pending→running transition, so a run with unresolvable
// capabilities fails immediately without ever appearing as running.
func (a *BoundAgent) checkCapabilities() error {
	for _, s := range a.policy.Capabilities.Sensors {
		if _, ok := a.toolsByName[s.Tool]; !ok {
			return fmt.Errorf("capability '%s' not found in MCP registry — verify the MCP server is registered and the tool exists", s.Tool)
		}
	}
	for _, act := range a.policy.Capabilities.Actuators {
		if _, ok := a.toolsByName[act.Tool]; !ok {
			return fmt.Errorf("capability '%s' not found in MCP registry — verify the MCP server is registered and the tool exists", act.Tool)
		}
	}
	for _, name := range a.policy.Capabilities.Feedback {
		if _, ok := a.toolsByName[name]; !ok {
			return fmt.Errorf("capability '%s' not found in MCP registry — verify the MCP server is registered and the tool exists", name)
		}
	}
	return nil
}

// Run executes the agent loop for a single run. It drives the Claude API
// until the model produces end_turn or the run limits are exceeded.
// Run returns nil on clean completion, or a wrapped error on failure.
// Run owns the run's status transitions: it moves the run to running on entry,
// complete on success, and failed on any error path.
func (a *BoundAgent) Run(ctx context.Context, runID string, triggerPayload string) error {
	// Run owns the AuditWriter lifecycle. Close is idempotent, so callers that
	// already held a reference to the writer can still call Close safely.
	defer a.audit.Close()

	// Fail fast before entering running state: every capability referenced by the
	// policy must resolve to a registered tool. Checked here (pending→failed) so the
	// run never briefly appears running when it has no chance of succeeding.
	if err := a.checkCapabilities(); err != nil {
		_ = a.audit.Write(ctx, Step{
			RunID:   runID,
			Type:    model.StepTypeError,
			Content: map[string]string{"message": err.Error(), "code": "MISSING_CAPABILITY"},
		})
		return a.failRun(ctx, err)
	}

	// Transition to running only after pre-flight checks pass. Use
	// context.Background() so the DB write lands even if the caller's context
	// is already cancelled — the loop will detect cancellation immediately.
	if err := a.sm.Transition(context.Background(), model.RunStatusRunning, ""); err != nil {
		// Best-effort: attempt to mark the run failed. If this also fails, log and
		// return the original transition error — the run will be cleaned up by the
		// startup scan on next restart.
		_ = a.failRun(context.Background(), err)
		return fmt.Errorf("transitioning run to running: %w", err)
	}

	// Extract granted tools for system prompt rendering.
	grantedTools := make([]model.GrantedTool, len(a.tools))
	for i, rt := range a.tools {
		grantedTools[i] = rt.GrantedTool
	}

	// Render system prompt (ADR-001: only granted tools are visible to the agent).
	systemPrompt := policy.RenderSystemPrompt(a.policy, grantedTools)

	// Build Anthropic tool definitions from narrowed schemas.
	anthropicTools := make([]anthropic.ToolUnionParam, 0, len(a.toolsByName))
	for dotName, entry := range a.toolsByName {
		inputSchema, err := buildToolInputSchema(entry.narrowedSchema)
		if err != nil {
			return a.failRun(ctx, fmt.Errorf("building tool schema for %s: %w", dotName, err))
		}
		tool := anthropic.ToolUnionParamOfTool(inputSchema, dotName)
		// Set description via the OfTool variant directly.
		if tool.OfTool != nil {
			tool.OfTool.Description = param.NewOpt(entry.tool.Description)
		}
		anthropicTools = append(anthropicTools, tool)
	}

	// Write capability snapshot step (ADR-018) — always the first step.
	// Use context.Background() so this initialization step always lands, even if
	// the caller's context was already cancelled before Run was entered.
	if err := a.audit.Write(context.Background(), Step{
		RunID:   runID,
		Type:    model.StepTypeCapabilitySnapshot,
		Content: grantedTools,
	}); err != nil {
		return a.failRun(ctx, fmt.Errorf("writing capability snapshot: %w", err))
	}

	// Initialize message history with the trigger payload.
	history := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(triggerPayload)),
	}

	var (
		totalTokens    int
		totalToolCalls int
	)

	maxTokensPerRun := a.policy.Agent.Limits.MaxTokensPerRun
	maxToolCalls := a.policy.Agent.Limits.MaxToolCallsPerRun

	for {
		// Respect context cancellation before each API call.
		// Use context.Background() for the audit write — the caller's context is
		// already done, so writing with it would silently drop the step.
		if err := ctx.Err(); err != nil {
			_ = a.audit.Write(context.Background(), Step{
				RunID:   runID,
				Type:    model.StepTypeError,
				Content: map[string]string{"message": "run cancelled", "code": "CANCELLED"},
			})
			return a.failRun(ctx, fmt.Errorf("agent run cancelled: %w", err))
		}

		// Determine per-call token limit.
		maxTokens := int64(8192)
		if maxTokensPerRun > 0 {
			remaining := int64(maxTokensPerRun - totalTokens)
			if remaining <= 0 {
				err := fmt.Errorf("token budget exceeded: %d tokens used, limit %d", totalTokens, maxTokensPerRun)
				slog.WarnContext(ctx, "token budget exceeded", "run_id", runID, "tokens_used", totalTokens, "limit", maxTokensPerRun)
				_ = a.audit.Write(ctx, Step{
					RunID:   runID,
					Type:    model.StepTypeError,
					Content: map[string]string{"message": err.Error(), "code": "TOKEN_BUDGET_EXCEEDED"},
				})
				return a.failRun(ctx, err)
			}
			if remaining < maxTokens {
				maxTokens = remaining
			}
		}

		resp, err := a.messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.ModelClaudeSonnet4_6,
			MaxTokens: maxTokens,
			System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
			Messages:  history,
			Tools:     anthropicTools,
		})
		if err != nil {
			// If the context was cancelled, the API error is a consequence of
			// cancellation. Write a CANCELLED step so the audit trail is clear.
			// Use context.Background() in all cases — ctx may already be done.
			if ctx.Err() != nil {
				_ = a.audit.Write(context.Background(), Step{
					RunID:   runID,
					Type:    model.StepTypeError,
					Content: map[string]string{"message": "run cancelled", "code": "CANCELLED"},
				})
			} else {
				_ = a.audit.Write(context.Background(), Step{
					RunID:   runID,
					Type:    model.StepTypeError,
					Content: map[string]string{"message": err.Error(), "code": "api_error"},
				})
			}
			return a.failRun(ctx, fmt.Errorf("claude API call: %w", err))
		}

		tokenCost := int(resp.Usage.InputTokens + resp.Usage.OutputTokens)
		totalTokens += tokenCost
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
					return a.failRun(ctx, fmt.Errorf("writing thought step: %w", err))
				}
			case anthropic.ToolUseBlock:
				totalToolCalls++
				if maxToolCalls > 0 && totalToolCalls > maxToolCalls {
					err := fmt.Errorf("tool call limit exceeded: %d calls, limit %d", totalToolCalls, maxToolCalls)
					slog.WarnContext(ctx, "tool call limit exceeded", "run_id", runID, "calls", totalToolCalls, "limit", maxToolCalls)
					_ = a.audit.Write(ctx, Step{
						RunID:   runID,
						Type:    model.StepTypeError,
						Content: map[string]string{"message": err.Error(), "code": "TOOL_CALL_LIMIT_EXCEEDED"},
					})
					return a.failRun(ctx, err)
				}

				var input map[string]any
				if err := json.Unmarshal(b.Input, &input); err != nil {
					return a.failRun(ctx, fmt.Errorf("unmarshalling tool input for %s: %w", b.Name, err))
				}

				resultStr, isError, err := a.handleToolCall(ctx, runID, b.ID, b.Name, input)
				if err != nil {
					return a.failRun(ctx, err) // handleToolCall already writes the error step
				}
				toolResults = append(toolResults, anthropic.NewToolResultBlock(b.ID, resultStr, isError))
			}
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
		history = append(history, anthropic.NewUserMessage(toolResults...))
	}
}

// handleToolCall dispatches a single tool call from the agent.
// For approval-gated actuators it suspends the run and waits for a decision
// before proceeding. This is the hard runtime guarantee (ADR-001).
// On error, it writes an error step and returns the error.
func (a *BoundAgent) handleToolCall(ctx context.Context, runID, _ /*toolUseID*/, toolName string, input map[string]any) (string, bool, error) {
	// Bail early if the context is already cancelled — no steps should be
	// written and no MCP call should be made.
	if err := ctx.Err(); err != nil {
		return "", false, fmt.Errorf("context cancelled before tool dispatch: %w", err)
	}

	entry, ok := a.toolsByName[toolName]
	if !ok {
		err := fmt.Errorf("tool not found: %s", toolName)
		_ = a.audit.Write(ctx, Step{
			RunID:   runID,
			Type:    model.StepTypeError,
			Content: map[string]string{"message": err.Error(), "code": "tool_error"},
		})
		return "", false, err
	}

	// Validate input against narrowed schema.
	if err := mcp.ValidateCall(entry.narrowedSchema, input); err != nil {
		_ = a.audit.Write(ctx, Step{
			RunID:   runID,
			Type:    model.StepTypeError,
			Content: map[string]string{"message": err.Error(), "code": "schema_violation"},
		})
		return "", false, fmt.Errorf("schema validation for %s: %w", toolName, err)
	}

	// Approval gating for actuators with approval: required.
	if entry.tool.Role == model.CapabilityRoleActuator && entry.tool.Approval == model.ApprovalModeRequired {
		if err := a.audit.Write(ctx, Step{
			RunID:   runID,
			Type:    model.StepTypeApprovalRequest,
			Content: map[string]any{"tool": toolName, "input": input},
		}); err != nil {
			return "", false, fmt.Errorf("writing approval request step: %w", err)
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
				err := fmt.Errorf("tool call %s rejected by operator", toolName)
				_ = a.audit.Write(ctx, Step{
					RunID:   runID,
					Type:    model.StepTypeError,
					Content: map[string]string{"message": err.Error(), "code": "approval_rejected"},
				})
				return "", false, err
			}
		case <-timeoutCh:
			if entry.tool.OnTimeout == model.OnTimeoutApprove {
				// Proceed with execution on timeout.
			} else {
				err := fmt.Errorf("approval timeout for tool %s", toolName)
				_ = a.audit.Write(ctx, Step{
					RunID:   runID,
					Type:    model.StepTypeError,
					Content: map[string]string{"message": err.Error(), "code": "approval_rejected"},
				})
				return "", false, err
			}
		case <-ctx.Done():
			return "", false, fmt.Errorf("context cancelled waiting for approval: %w", ctx.Err())
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
		if ctx.Err() != nil {
			_ = a.audit.Write(context.Background(), Step{
				RunID:   runID,
				Type:    model.StepTypeError,
				Content: map[string]string{"message": "run cancelled", "code": "CANCELLED"},
			})
		} else {
			_ = a.audit.Write(context.Background(), Step{
				RunID:   runID,
				Type:    model.StepTypeError,
				Content: map[string]string{"message": err.Error(), "code": "tool_error"},
			})
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

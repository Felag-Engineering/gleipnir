// Package agent implements the BoundAgent runner — the core Claude API loop
// with hard capability enforcement, approval interception, and audit writing.
package agent

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
)

// BoundAgent executes a single policy run. It owns the Claude API loop,
// dispatches tool calls to MCP clients, intercepts approval-gated actuators,
// and writes every step to the audit trail via AuditWriter.
type BoundAgent struct {
	runID  string
	policy *model.ParsedPolicy
	tools  []mcp.ResolvedTool
	claude *anthropic.Client
	audit  *AuditWriter
	// approvalCh receives the operator's decision when a run is suspended
	// waiting_for_approval. Sent by the approval handler in internal/trigger.
	approvalCh <-chan ApprovalDecision
}

// ApprovalDecision carries the operator's response to an approval request.
type ApprovalDecision struct {
	Approved bool
	Note     string
}

// Config holds the dependencies needed to construct a BoundAgent.
type Config struct {
	RunID      string
	Policy     *model.ParsedPolicy
	Tools      []mcp.ResolvedTool
	Claude     *anthropic.Client
	Audit      *AuditWriter
	ApprovalCh <-chan ApprovalDecision
}

// New returns a BoundAgent ready to run.
func New(cfg Config) *BoundAgent {
	return &BoundAgent{
		runID:      cfg.RunID,
		policy:     cfg.Policy,
		tools:      cfg.Tools,
		claude:     cfg.Claude,
		audit:      cfg.Audit,
		approvalCh: cfg.ApprovalCh,
	}
}

// Run executes the agent loop for a single run. It drives the Claude API
// until the model produces a stop_sequence or the run limits are exceeded.
// Run returns nil on clean completion, or a wrapped error on failure.
// The caller is responsible for updating the run's terminal status in the DB.
func (a *BoundAgent) Run(ctx context.Context, triggerPayload string) error {
	// TODO:
	// 1. Render system prompt via policy.RenderSystemPrompt
	// 2. Build initial message from triggerPayload
	// 3. Register only granted tools with the Claude API (hard enforcement)
	// 4. Loop: call claude.Messages.Create, handle each content block:
	//    - thinking → write thought step
	//    - text → write thought step
	//    - tool_use → dispatch via handleToolCall
	// 5. After each LLM response: check token budget and tool call count
	// 6. Loop ends when stop_reason == "end_turn" or limits exceeded
	_ = fmt.Errorf // prevent unused import during stub phase
	panic("not implemented")
}

// handleToolCall dispatches a single tool call from the agent.
// For approval-gated actuators, it suspends the run and waits for a decision
// before proceeding. This is the hard runtime guarantee (ADR-001).
func (a *BoundAgent) handleToolCall(ctx context.Context, toolName string, input map[string]any) (string, error) {
	// TODO:
	// 1. Look up tool in a.tools by name
	// 2. If actuator with approval: required → write approval_request step,
	//    update run status to waiting_for_approval, block on a.approvalCh
	// 3. On rejection/timeout → return error to terminate run
	// 4. On approval (or no gate needed) → call tool.Client.CallTool
	// 5. Write tool_call and tool_result steps via a.audit
	panic("not implemented")
}

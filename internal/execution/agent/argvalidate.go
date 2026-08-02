// Package agent — this file holds pre-dispatch exact argument enforcement
// against a tool's canonical schema (spec §10 step 3, #744), isolated from
// tools.go and agent.go so #745's rebase onto tools.go stays a two-line diff.
package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// compileArgValidator compiles rt's canonical schema (narrowed to rt.Params
// per ADR-017) into an ArgValidator, once per tool at run start. Returns nil
// — falling back to today's key-presence-only ValidateCall — when exact
// enforcement is unavailable:
//
//   - rt.CanonicalSchema is empty: #738 stores NULL when discovery-time
//     canonicalization failed, and this is an expected steady state (already
//     WARN-logged at discovery), so no additional log here.
//   - the canonical schema fails to compile (bad $ref, invalid pattern regex,
//     an unrecognized dialect, ...): logged at WARN naming the tool, since an
//     otherwise-healthy tool silently losing exact enforcement is worth an
//     operator's attention.
//
// Uses plain log/slog rather than logctx: no context is available here — New()
// and buildResolvedToolMap run before a run's context exists. Precedent:
// internal/mcp/canonical.go and this package's feedback.go.
func compileArgValidator(rt mcp.ResolvedTool, dotName string) *mcp.ArgValidator {
	if len(rt.CanonicalSchema) == 0 {
		return nil
	}

	v, err := mcp.NewArgValidator(rt.CanonicalSchema, rt.Params)
	if err != nil {
		// Never log the schema bytes — err may be a wrapped jsonschema
		// compile error, which is fine to log, but the schema itself is not.
		slog.Warn("canonical schema failed to compile; falling back to key-presence argument validation",
			"tool", dotName, "err", err)
		return nil
	}
	return v
}

// schemaViolation renders a structural tool-call violation as the
// codebase's structural-error shape: a tagged error step for the audit
// trail, a tool_result step with is_error: true for the agent to read, and
// (msg, true, nil) so the caller returns it as a correctable error instead
// of failing the run. Mirrors the plugin generation guard (agent.go) and MCP
// transport failures (agent.go).
//
// Two producers, with different audit-trail shapes:
//   - gate 2, ArgValidator (exact enforcement, #744): runs BEFORE the
//     tool_call audit step is written, so its violation's run has no
//     tool_call step for the rejected call.
//   - CallTool's *mcp.HeaderParamError (#747): reached from inside
//     entry.tool.Client.CallTool, which is dispatched AFTER the tool_call
//     step is already written (agent.go). So a rejected x-mcp-header
//     declaration's run DOES have a tool_call step — that is correct, and
//     matches how every other MCP transport failure is already audited; it
//     is not "behavior unchanged" relative to the ArgValidator case.
//
// NOT used by the ADR-017 key-presence gate (gate 1, mcp.ValidateCall) —
// that gate fails the run instead, so its violation reaches the operator
// attention queue (see the comment above the two gates in handleToolCall).
func (a *BoundAgent) schemaViolation(ctx context.Context, runID, toolName string, verr error) (string, bool, error) {
	msg := fmt.Sprintf("tool %s: %s", toolName, verr.Error())

	a.logAuditError(ctx, runID, msg, model.ErrorCodeSchemaViolation)

	if err := a.audit.Write(ctx, Step{
		RunID: runID,
		Type:  model.StepTypeToolResult,
		Content: map[string]any{
			"tool_name": toolName,
			"output":    msg,
			"is_error":  true,
		},
	}); err != nil {
		return "", false, fmt.Errorf("writing schema violation tool_result: %w", err)
	}

	return msg, true, nil
}

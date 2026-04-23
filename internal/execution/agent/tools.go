// Package agent — this file holds tool-definition and capability-check glue.
// Extracting it from agent.go keeps the orchestrator thin.
package agent

import (
	"encoding/json"
	"fmt"

	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
)

// resolvedToolEntry holds a ResolvedTool paired with its narrowed JSON schema.
type resolvedToolEntry struct {
	tool           mcp.ResolvedTool
	narrowedSchema json.RawMessage
}

// buildResolvedToolMap constructs the name→entry map used at runtime to dispatch
// tool calls. It applies policy-level parameter scoping (ADR-017) by narrowing
// each tool's input schema.
func buildResolvedToolMap(tools []mcp.ResolvedTool) (map[string]resolvedToolEntry, error) {
	toolsByName := make(map[string]resolvedToolEntry, len(tools))
	for _, rt := range tools {
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
	return toolsByName, nil
}

// checkCapabilities verifies every capability reference in the policy resolves
// to a tool registered at BoundAgent construction time. Called at the start of
// Run(), before the pending→running transition, so a run with unresolvable
// capabilities fails immediately without ever appearing as running.
func (a *BoundAgent) checkCapabilities() error {
	// Verify every tool capability references a tool registered at construction
	// time. The feedback channel (FeedbackConfig) is not an MCP tool and
	// requires no registry check — it is injected by the runtime when Enabled
	// is true.
	for _, t := range a.policy.Capabilities.Tools {
		if _, ok := a.toolsByName[t.Tool]; !ok {
			return fmt.Errorf("capability '%s' not found in MCP registry — verify the MCP server is registered and the tool exists", t.Tool)
		}
	}
	return nil
}

// buildToolDefinitions builds the provider-neutral tool definitions from the
// agent's registered tools. The LLMClient handles provider-specific name
// sanitization and schema formatting. When feedback is enabled, the synthetic
// gleipnir.ask_operator tool is appended so the LLM can call it directly.
func (a *BoundAgent) buildToolDefinitions() []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(a.toolsByName))
	for dotName, entry := range a.toolsByName {
		defs = append(defs, llm.ToolDefinition{
			Name:        dotName,
			Description: entry.tool.Description,
			InputSchema: entry.narrowedSchema,
		})
	}
	if a.policy.Capabilities.Feedback.Enabled {
		defs = append(defs, askOperatorToolDefinition())
	}
	return defs
}

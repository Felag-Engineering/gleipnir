// Package agent — this file holds tool-definition and capability-check glue.
// Extracting it from agent.go keeps the orchestrator thin.
package agent

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// pluginToolSource identifies the plugin instance and generation captured at run
// start. Used by handleToolCall to detect when the plugin has been replaced.
type pluginToolSource struct {
	InstanceName string
	Generation   int64
}

// resolvedToolEntry holds a ResolvedTool paired with its narrowed JSON schema.
// narrowedSchema is the policy-scoped view of the tool's canonical input
// schema (raw when no canonical form is stored) and is what the LLM sees;
// tool.InputSchema is the raw schema of record.
// pluginSource is non-nil for plugin-backed tools; nil for MCP-source tools.
// argValidator is the compiled exact-enforcement validator; nil when
// unavailable (see argvalidate.go).
type resolvedToolEntry struct {
	tool           mcp.ResolvedTool
	narrowedSchema json.RawMessage
	pluginSource   *pluginToolSource // nil for MCP-source tools
	argValidator   *mcp.ArgValidator // nil when exact enforcement is unavailable; see argvalidate.go
}

// sourceString returns the source identifier for the tool: "plugin:<name>@<gen>"
// for plugin-source tools, or "mcp:<ServerName>" for MCP-source tools.
func sourceString(entry resolvedToolEntry) string {
	if entry.pluginSource != nil {
		return fmt.Sprintf("plugin:%s@%d", entry.pluginSource.InstanceName, entry.pluginSource.Generation)
	}
	return "mcp:" + entry.tool.ServerName
}

// buildResolvedToolMap constructs the name→entry map for MCP-source tools. It
// applies policy-level parameter scoping (ADR-017) by narrowing each tool's
// stored canonical schema, falling back to the raw schema only when no
// canonical form is stored (see mcp.ResolvedTool.SchemaForNarrowing).
// Plugin-source tools are added by New() in a separate loop because they
// carry different fields (pluginSource pointer, schema from map[string]any
// vs json.RawMessage, snapshot side-effects).
func buildResolvedToolMap(tools []mcp.ResolvedTool) (map[string]resolvedToolEntry, error) {
	toolsByName := make(map[string]resolvedToolEntry, len(tools))
	for _, rt := range tools {
		dotName := rt.ServerName + "." + rt.ToolName

		narrowed, err := mcp.NarrowSchema(rt.SchemaForNarrowing(), rt.Params)
		if err != nil {
			return nil, fmt.Errorf("narrowing schema for tool %s.%s: %w", rt.ServerName, rt.ToolName, err)
		}
		toolsByName[dotName] = resolvedToolEntry{
			tool:           rt,
			narrowedSchema: narrowed,
			argValidator:   compileArgValidator(rt, dotName),
		}
	}
	return toolsByName, nil
}

// buildPluginToolEntries produces the dispatch-side map entries and the
// snapshot-side granted-tool slice from a single pass over the plugin tool
// configs. New() calls it once and merges the returned map into toolsByName,
// keeping both structures derived from the same source (plan decision (f)).
//
// Panics are intentionally NOT in this helper — they live in New() as a gate
// on PluginRegistrar/PluginDispatcher, before this function is called.
func buildPluginToolEntries(pts []PluginToolEntry) (map[string]resolvedToolEntry, []model.GrantedTool, error) {
	entries := make(map[string]resolvedToolEntry, len(pts))
	granted := make([]model.GrantedTool, 0, len(pts))

	for _, pt := range pts {
		dotName := pt.InstanceName + "." + pt.ToolName

		schemaJSON, err := json.Marshal(pt.Schema)
		if err != nil {
			return nil, nil, fmt.Errorf("marshaling schema for plugin tool %s: %w", dotName, err)
		}

		// Apply policy-level parameter scoping (ADR-017) to plugin tools, exactly
		// as buildResolvedToolMap does for MCP tools. Unlike the MCP path, there
		// is no SchemaForNarrowing fallback here: plugin tools have no canonical
		// form at all (the ResolvedTool literal built below never populates
		// CanonicalSchema, per that field's doc), so schemaJSON is the only
		// schema available. Plugin-side scoping onto a canonical form is out of
		// scope for this issue.
		narrowed, err := mcp.NarrowSchema(schemaJSON, pt.Params)
		if err != nil {
			return nil, nil, fmt.Errorf("narrowing schema for plugin tool %s: %w", dotName, err)
		}

		entry := resolvedToolEntry{
			tool: mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{
					ServerName: "", // no MCP server; plugin dispatch is via pluginSource
					ToolName:   pt.ToolName,
					Approval:   pt.Approval,
					Timeout:    pt.Timeout,
					Params:     pt.Params,
				},
				Client:      nil, // real dispatch wired in #198
				Description: pt.Description,
				InputSchema: schemaJSON,
			},
			narrowedSchema: narrowed,
			pluginSource: &pluginToolSource{
				InstanceName: pt.InstanceName,
				Generation:   pt.Generation,
			},
		}
		entries[dotName] = entry

		granted = append(granted, model.GrantedTool{
			ServerName: "",
			ToolName:   pt.ToolName,
			Approval:   pt.Approval,
			Params:     pt.Params,
			Source:     sourceString(entry),
		})
	}

	return entries, granted, nil
}

// buildCapabilitySnapshotTools assembles the ordered list of granted tools for
// the ADR-018 capability snapshot. Ordering is MCP tools → synthetic
// gleipnir.ask_operator (when feedback is enabled) → plugin tools.
//
// pluginGranted carries Source strings already set at New() time (via
// buildPluginToolEntries), so they pass through unchanged. MCP tool Source
// strings are computed here via sourceString.
func buildCapabilitySnapshotTools(tools []mcp.ResolvedTool, pluginGranted []model.GrantedTool, feedbackEnabled bool) []model.GrantedTool {
	out := make([]model.GrantedTool, len(tools), len(tools)+len(pluginGranted))
	for i, rt := range tools {
		gt := rt.GrantedTool // copy: GrantedTool is a value type
		gt.Source = sourceString(resolvedToolEntry{tool: rt})
		out[i] = gt
	}
	// Include the synthetic ask_operator tool in the capability snapshot when
	// feedback is enabled, so the audit trail reflects the full set of tools
	// available to the agent at run start. Source is intentionally empty for
	// synthetic tools (they have no MCP server or plugin instance).
	if feedbackEnabled {
		out = append(out, model.GrantedTool{
			ServerName: "gleipnir",
			ToolName:   "ask_operator",
		})
	}
	// Append plugin-source tools; their Source strings were set at New() time.
	out = append(out, pluginGranted...)
	return out
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
	//
	// Note: plugin tools come from Config.PluginTools (loaded into toolsByName by
	// New()), NOT from policy capabilities — so they are correctly exempt from this
	// check. A plugin tool that fails namespace reservation never reaches toolsByName.
	for _, t := range a.policy.Capabilities.Tools {
		if _, ok := a.toolsByName[t.Tool]; !ok {
			return fmt.Errorf("capability %q is not a registered tool — verify the MCP server or plugin providing it is installed and the tool exists", t.Tool)
		}
	}
	return nil
}

// buildToolDefinitions builds the provider-neutral tool definitions from the
// agent's registered tools. The LLMClient handles provider-specific name
// sanitization and schema formatting. When feedback is enabled, the synthetic
// gleipnir.ask_operator tool is appended so the LLM can call it directly.
//
// Tools are emitted sorted by dot-name (ascending), not in toolsByName's map
// iteration order. Go randomizes map iteration order per run, so the tool
// array a provider received previously changed shape on every single run of
// the same policy — sorting makes it byte-stable instead, which is a
// prerequisite for a provider's prompt cache to ever treat two runs' tool
// arrays as identical (mcp-realignment-spec.md §11). This is a distinct
// ordering from buildCapabilitySnapshotTools, which deliberately keeps
// policy grant order for the ADR-018 audit snapshot — the two orderings are
// allowed to differ; nothing requires them to match. askOperator is appended
// after the sorted block, not merged into it, because it is a synthetic tool
// with no dot-name in the <source>.<tool> namespace those sorted entries
// come from.
func (a *BoundAgent) buildToolDefinitions() []llm.ToolDefinition {
	dotNames := make([]string, 0, len(a.toolsByName))
	for dotName := range a.toolsByName {
		dotNames = append(dotNames, dotName)
	}
	sort.Strings(dotNames)

	defs := make([]llm.ToolDefinition, 0, len(a.toolsByName))
	for _, dotName := range dotNames {
		entry := a.toolsByName[dotName]
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

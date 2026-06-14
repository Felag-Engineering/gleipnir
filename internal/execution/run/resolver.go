package run

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// ResolvedToolSet holds the outputs of a single ResolveCapabilities call,
// split by source kind. MCPTools and PluginTools are kept as separate typed
// slices (not a pre-merged map) because the merge and schema narrowing happen
// inside the agent package, where the unexported resolvedToolEntry and
// pluginToolSource types live. The resolver's job is classify + per-source
// resolve; the merge stays in agent.New.
type ResolvedToolSet struct {
	MCPTools    []mcp.ResolvedTool
	PluginTools []agent.PluginToolEntry
}

// ToolResolver is the single entry point for tool resolution. Callers pass
// the policy's raw capability grants and receive back the fully-resolved tool
// sets for each source. Classification, per-source resolution, and the merge
// inputs are all encapsulated here — callers have no knowledge of the routing.
type ToolResolver interface {
	ResolveCapabilities(ctx context.Context, grants []model.ToolCapability) (ResolvedToolSet, error)
}

// registryResolver is the subset of mcp.Registry used by DefaultToolResolver.
// Defined here as an interface so tests can supply a stub without importing
// internal/mcp directly.
type registryResolver interface {
	ResolveForPolicy(ctx context.Context, p *model.ParsedPolicy) ([]mcp.ResolvedTool, error)
}

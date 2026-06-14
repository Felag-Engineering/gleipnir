package run

import (
	"context"
	"errors"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// PluginToolResolver resolves plugin-sourced tool grants into agent-ready
// entries. The implementation looks up the manifest, finds each tool's
// description and schema, and reads the current generation from the registrar.
type PluginToolResolver interface {
	ResolvePluginTools(ctx context.Context, grants []model.ToolCapability) ([]agent.PluginToolEntry, error)
}

// ToolSourceClassifier determines whether a dot-name tool grant should be
// resolved via MCP or the plugin path. The lookup is static — based on the
// installed plugin instance row and its manifest snapshot, NOT on whether the
// instance's subprocess is currently running (see #399). It does DB + manifest
// reads, so it takes a context and can return an error.
type ToolSourceClassifier interface {
	IsPluginTool(ctx context.Context, dotName string) (bool, error)
}

// DefaultToolResolver is the standard ToolResolver implementation. It
// classifies each grant as MCP or plugin-sourced, dispatches to the
// appropriate resolver, and returns the combined results. When classifier or
// pluginResolver is nil (plugins disabled), all grants route to the MCP path.
type DefaultToolResolver struct {
	registry       registryResolver
	classifier     ToolSourceClassifier
	pluginResolver PluginToolResolver
}

// NewDefaultToolResolver constructs a DefaultToolResolver. classifier and
// pluginResolver may both be nil when plugins are disabled; all grants then
// route to the MCP path, preserving the pre-plugin behaviour exactly.
func NewDefaultToolResolver(registry registryResolver, classifier ToolSourceClassifier, pluginResolver PluginToolResolver) *DefaultToolResolver {
	return &DefaultToolResolver{
		registry:       registry,
		classifier:     classifier,
		pluginResolver: pluginResolver,
	}
}

// ResolveCapabilities implements ToolResolver. It splits the grants by source
// (via classifier), resolves each group, and returns the combined result. All
// four error paths (classify error, MCP-resolve error, nil-resolver with plugin
// grants, plugin-resolve error) return the error unwrapped — the launcher
// applies a single fail-run transition on any non-nil error.
func (r *DefaultToolResolver) ResolveCapabilities(ctx context.Context, grants []model.ToolCapability) (ResolvedToolSet, error) {
	var mcpGrants, pluginGrants []model.ToolCapability
	for _, t := range grants {
		isPlugin := false
		if r.classifier != nil {
			var classErr error
			isPlugin, classErr = r.classifier.IsPluginTool(ctx, t.Tool)
			if classErr != nil {
				// A classification read failed (e.g. the manifest snapshot could not
				// be loaded). Return loudly rather than silently misrouting the grant
				// to the MCP path.
				return ResolvedToolSet{}, fmt.Errorf("classify tool %q: %w", t.Tool, classErr)
			}
		}
		if isPlugin {
			pluginGrants = append(pluginGrants, t)
		} else {
			mcpGrants = append(mcpGrants, t)
		}
	}

	// Resolve MCP tools. When mcpGrants is empty we skip the DB round-trip
	// entirely — ResolveForPolicy with an empty list is a no-op but the shallow
	// copy and call are still wasted work.
	var resolvedTools []mcp.ResolvedTool
	if len(mcpGrants) > 0 {
		// Construct a minimal ParsedPolicy carrying only the grants for this
		// resolution call; ResolveForPolicy reads only Capabilities.Tools.
		// Shallow copy avoids mutating the caller's policy if they passed one in.
		mcpPolicy := &model.ParsedPolicy{}
		mcpPolicy.Capabilities.Tools = mcpGrants
		var mcpErr error
		resolvedTools, mcpErr = r.registry.ResolveForPolicy(ctx, mcpPolicy)
		if mcpErr != nil {
			return ResolvedToolSet{}, mcpErr
		}
	}

	// Resolve plugin tools. When no plugin grants exist this is a no-op. When
	// grants exist but pluginResolver is nil, the policy references a plugin
	// tool while the plugin subsystem is not enabled — fail clearly rather than
	// silently dropping the grant.
	var pluginToolEntries []agent.PluginToolEntry
	if len(pluginGrants) > 0 {
		if r.pluginResolver == nil {
			return ResolvedToolSet{}, errors.New("plugin tools granted but plugin subsystem is not enabled")
		}
		var pluginErr error
		pluginToolEntries, pluginErr = r.pluginResolver.ResolvePluginTools(ctx, pluginGrants)
		if pluginErr != nil {
			return ResolvedToolSet{}, pluginErr
		}
	}

	return ResolvedToolSet{MCPTools: resolvedTools, PluginTools: pluginToolEntries}, nil
}

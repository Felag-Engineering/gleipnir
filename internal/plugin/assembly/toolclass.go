package assembly

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/manifest"
)

// classifierStore is the DB surface the classifier reads. *db.Queries
// satisfies it.
type classifierStore interface {
	GetPluginInstanceByGlobalName(ctx context.Context, instanceName string) (db.PluginInstance, error)
	GetPluginByID(ctx context.Context, id string) (db.Plugin, error)
	GetMCPServerByPluginInstance(ctx context.Context, pluginInstanceID *string) (db.McpServer, error)
	GetMCPToolByServerAndName(ctx context.Context, arg db.GetMCPToolByServerAndNameParams) (db.McpTool, error)
}

// managedToolClassifier is the v2 runpkg.ToolSourceClassifier.
//
// A managed plugin's tools are ordinary MCP tools on an ordinary mcp_servers
// row, so a tool that passes every check below is NOT a "plugin tool" in the
// v1 sense (agent-side dispatch through a plugin pool): the answer is false and
// DefaultToolResolver routes it to the MCP registry. What this type adds is
// the refusal. It decides from the installed instance row and the consented
// manifest, never from the shape of the name, so a grant whose source is a
// plugin instance's name can only ever reach that instance's own managed
// endpoint:
//
//   - source is not an installed instance: external MCP server, (false, nil).
//   - source IS an installed instance: it must belong to an active plugin
//     whose v2 manifest declares the tool_provider profile, have a managed
//     endpoint row named after it, and that endpoint must have discovered the
//     tool. Anything short of that is an error, not a fall-through to the MCP
//     path: falling through is exactly how a name that merely LOOKS like
//     `<instance>.<tool>` would be resolved against whatever else is
//     registered under it.
//
// The v2 manifest does not enumerate tools (they come from tools/list), so the
// per-tool declaration check v1 made against the manifest becomes a check
// against the managed endpoint's discovered tools.
type managedToolClassifier struct {
	q classifierStore
}

func (c *managedToolClassifier) IsPluginTool(ctx context.Context, dotName string) (bool, error) {
	source, toolName, ok := splitDotName(dotName)
	if !ok {
		return false, nil
	}

	inst, err := c.q.GetPluginInstanceByGlobalName(ctx, source)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup plugin instance %q: %w", source, err)
	}

	plugin, err := c.q.GetPluginByID(ctx, inst.PluginID)
	if err != nil {
		return false, fmt.Errorf("plugin tool %q: load plugin for instance %q: %w", dotName, source, err)
	}
	if plugin.Status != string(model.PluginStatusActive) {
		return false, fmt.Errorf("plugin tool %q: plugin %q is %s, not active", dotName, plugin.Name, plugin.Status)
	}

	snap, err := manifest.Read([]byte(plugin.ManifestSnapshot))
	if err != nil {
		return false, fmt.Errorf("plugin tool %q: manifest for instance %q: %w", dotName, source, err)
	}
	if snap.Version != 2 {
		return false, fmt.Errorf("plugin tool %q: instance %q has a v%d manifest; only v2 plugins are served here", dotName, source, snap.Version)
	}
	if snap.Profiles.ToolProvider == nil {
		return false, fmt.Errorf("plugin tool %q: instance %q does not declare the tool_provider profile", dotName, source)
	}

	srv, err := c.q.GetMCPServerByPluginInstance(ctx, &inst.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("plugin tool %q: instance %q has no managed endpoint", dotName, source)
	}
	if err != nil {
		return false, fmt.Errorf("plugin tool %q: lookup managed endpoint: %w", dotName, err)
	}
	if !mcp.IsManaged(srv) || srv.Name != source {
		return false, fmt.Errorf("plugin tool %q: endpoint %q is not instance %q's managed endpoint", dotName, srv.Name, source)
	}

	tool, err := c.q.GetMCPToolByServerAndName(ctx, db.GetMCPToolByServerAndNameParams{
		ServerName: srv.Name,
		ToolName:   toolName,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("plugin tool %q: instance %q has not discovered a tool named %q", dotName, source, toolName)
	}
	if err != nil {
		return false, fmt.Errorf("plugin tool %q: lookup tool: %w", dotName, err)
	}
	if tool.ServerID != srv.ID {
		return false, fmt.Errorf("plugin tool %q: tool %q belongs to a different endpoint than instance %q's", dotName, toolName, source)
	}
	return false, nil
}

// splitDotName splits "source.tool"; ok is false when either half is empty
// or the dot is missing.
func splitDotName(dotName string) (source, tool string, ok bool) {
	source, tool, found := strings.Cut(dotName, ".")
	if !found || source == "" || tool == "" {
		return "", "", false
	}
	return source, tool, true
}

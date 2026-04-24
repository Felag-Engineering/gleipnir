package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// ResolvedTool pairs a granted tool's model metadata with a ready Client
// targeting its server. Used by the agent runner to call tools.
type ResolvedTool struct {
	model.GrantedTool
	Client      *Client
	Description string          // tool description from the MCP registry
	InputSchema json.RawMessage // raw JSON schema from the MCP tool record
}

// ToolDiff describes the set of changes detected between two successive tool
// discovery snapshots for a server. Names are sorted for deterministic output.
type ToolDiff struct {
	Added    []string
	Removed  []string
	Modified []string
}

// Registry resolves policy capability references to live MCP clients.
// It reads server and tool records from the DB and builds Client instances
// on demand.
type Registry struct {
	queries    *db.Queries
	mcpTimeout time.Duration
}

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// WithMCPTimeout sets the HTTP timeout applied to every MCP Client created
// by the Registry. When zero, the Client default (30 s) is used.
func WithMCPTimeout(d time.Duration) RegistryOption {
	return func(r *Registry) {
		r.mcpTimeout = d
	}
}

// NewRegistry returns a Registry backed by the given sqlc Queries.
func NewRegistry(queries *db.Queries, opts ...RegistryOption) *Registry {
	r := &Registry{queries: queries}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// newClient creates an MCP Client for the given server name and URL, applying
// the Registry's mcpTimeout when it is non-zero. serverName is stored on the
// Client so CallTool can use it as a Prometheus label.
func (r *Registry) newClient(name, url string) *Client {
	var cl *Client
	if r.mcpTimeout > 0 {
		cl = NewClient(url, WithTimeout(r.mcpTimeout))
	} else {
		cl = NewClient(url)
	}
	cl.serverName = name
	return cl
}

// splitToolName splits a dot-notation tool name (e.g. "my-server.read_pods")
// into its server and tool components. Both parts must be non-empty.
func splitToolName(dotName string) (serverName, toolName string, err error) {
	parts := strings.SplitN(dotName, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("tool name %q must be in server.tool dot-notation", dotName)
	}
	return parts[0], parts[1], nil
}

// ResolveForPolicy resolves the granted tool list for a parsed policy,
// returning a ResolvedTool for each entry in capabilities.tools. Returns
// an error if any tool reference is not found in the DB — this is the
// fail-fast check at run start.
func (r *Registry) ResolveForPolicy(ctx context.Context, p *model.ParsedPolicy) ([]ResolvedTool, error) {
	var result []ResolvedTool
	clients := make(map[string]*Client)

	for _, t := range p.Capabilities.Tools {
		serverName, toolName, err := splitToolName(t.Tool)
		if err != nil {
			return nil, fmt.Errorf("resolve tool %q: %w", t.Tool, err)
		}

		tool, err := r.queries.GetMCPToolByServerAndName(ctx, db.GetMCPToolByServerAndNameParams{
			ServerName: serverName,
			ToolName:   toolName,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("tool %q not found in registry", t.Tool)
			}
			return nil, fmt.Errorf("look up tool %q: %w", t.Tool, err)
		}

		if tool.Enabled == 0 {
			return nil, fmt.Errorf("tool %q on server %q is disabled", toolName, serverName)
		}

		srv, err := r.queries.GetMCPServer(ctx, tool.ServerID)
		if err != nil {
			return nil, fmt.Errorf("get server for tool %q: %w", t.Tool, err)
		}

		var timeout time.Duration
		if t.Timeout != "" {
			timeout, err = time.ParseDuration(t.Timeout)
			if err != nil {
				return nil, fmt.Errorf("parse timeout for tool %q: %w", t.Tool, err)
			}
		}

		cl, ok := clients[srv.Url]
		if !ok {
			cl = r.newClient(serverName, srv.Url)
			clients[srv.Url] = cl
		}

		result = append(result, ResolvedTool{
			GrantedTool: model.GrantedTool{
				ServerName: serverName,
				ToolName:   toolName,
				Approval:   t.Approval,
				Timeout:    timeout,
				OnTimeout:  t.OnTimeout,
				Params:     t.Params,
			},
			Client:      cl,
			Description: tool.Description,
			InputSchema: json.RawMessage(tool.InputSchema),
		})
	}

	return result, nil
}

// ResolveToolByName resolves a single tool by dot-notation name and returns
// a ready MCP Client plus the bare tool name. Used by the poll trigger engine
// to call a tool outside the agent runtime context.
func (r *Registry) ResolveToolByName(ctx context.Context, dotName string) (*Client, string, error) {
	serverName, toolName, err := splitToolName(dotName)
	if err != nil {
		return nil, "", fmt.Errorf("resolve tool %q: %w", dotName, err)
	}

	tool, err := r.queries.GetMCPToolByServerAndName(ctx, db.GetMCPToolByServerAndNameParams{
		ServerName: serverName,
		ToolName:   toolName,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", fmt.Errorf("tool %q not found in registry", dotName)
		}
		return nil, "", fmt.Errorf("look up tool %q: %w", dotName, err)
	}

	if tool.Enabled == 0 {
		return nil, "", fmt.Errorf("tool %q on server %q is disabled", toolName, serverName)
	}

	srv, err := r.queries.GetMCPServer(ctx, tool.ServerID)
	if err != nil {
		return nil, "", fmt.Errorf("get server for tool %q: %w", dotName, err)
	}

	return r.newClient(serverName, srv.Url), toolName, nil
}

// RegisterServer stores a new MCP server record, discovers its tools via the
// MCP client, and upserts all discovered tools into mcp_tools.
// last_discovered_at is intentionally left NULL here — it is set only by RefreshTools.
func (r *Registry) RegisterServer(ctx context.Context, name, url string) error {
	if err := ValidateServerURL(ctx, url); err != nil {
		return fmt.Errorf("invalid server url: %w", err)
	}

	serverID := model.NewULID()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if _, err := r.queries.CreateMCPServer(ctx, db.CreateMCPServerParams{
		ID:        serverID,
		Name:      name,
		Url:       url,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("create mcp server: %w", err)
	}

	tools, err := r.newClient(name, url).DiscoverTools(ctx)
	if err != nil {
		return fmt.Errorf("discover tools for server %q: %w", name, err)
	}

	for _, t := range tools {
		if _, err := r.queries.UpsertMCPTool(ctx, db.UpsertMCPToolParams{
			ID:          model.NewULID(),
			ServerID:    serverID,
			Name:        t.Name,
			Description: t.Description,
			InputSchema: string(t.InputSchema),
			CreatedAt:   now,
		}); err != nil {
			return fmt.Errorf("upsert tool %q: %w", t.Name, err)
		}
	}

	return nil
}

// RefreshTools re-discovers tools for a registered server, computes the diff
// against the current DB state, upserts all fresh tools, deletes tools that
// have disappeared, and updates last_discovered_at.
func (r *Registry) RefreshTools(ctx context.Context, serverID string) (ToolDiff, error) {
	// Fetch current tool state from DB so we can compute the diff and
	// preserve tool IDs for existing tools.
	oldTools, err := r.queries.ListMCPToolsByServer(ctx, serverID)
	if err != nil {
		return ToolDiff{}, fmt.Errorf("list existing tools: %w", err)
	}

	oldByName := make(map[string]db.McpTool, len(oldTools))
	for _, t := range oldTools {
		oldByName[t.Name] = t
	}

	srv, err := r.queries.GetMCPServer(ctx, serverID)
	if err != nil {
		return ToolDiff{}, fmt.Errorf("get mcp server %q: %w", serverID, err)
	}

	freshTools, err := r.newClient(srv.Name, srv.Url).DiscoverTools(ctx)
	if err != nil {
		return ToolDiff{}, fmt.Errorf("discover tools for server %q: %w", serverID, err)
	}

	freshByName := make(map[string]Tool, len(freshTools))
	for _, t := range freshTools {
		freshByName[t.Name] = t
	}

	// Compute diff.
	var diff ToolDiff
	for name := range freshByName {
		if _, exists := oldByName[name]; !exists {
			diff.Added = append(diff.Added, name)
		}
	}
	for name, old := range oldByName {
		fresh, exists := freshByName[name]
		if !exists {
			diff.Removed = append(diff.Removed, name)
			continue
		}
		if old.Description != fresh.Description || old.InputSchema != string(fresh.InputSchema) {
			diff.Modified = append(diff.Modified, name)
		}
	}

	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Modified)

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Upsert all fresh tools. Preserve the existing ID for tools already in the
	// DB so foreign key references (e.g. in audit steps) remain stable.
	// ON CONFLICT does not touch the enabled column — operator-set disable state
	// survives rediscovery (see the UpsertMCPTool query for the ON CONFLICT clause).
	for _, t := range freshTools {
		toolID := model.NewULID()
		if old, exists := oldByName[t.Name]; exists {
			toolID = old.ID
		}

		if _, err := r.queries.UpsertMCPTool(ctx, db.UpsertMCPToolParams{
			ID:          toolID,
			ServerID:    serverID,
			Name:        t.Name,
			Description: t.Description,
			InputSchema: string(t.InputSchema),
			CreatedAt:   now,
		}); err != nil {
			return ToolDiff{}, fmt.Errorf("upsert tool %q: %w", t.Name, err)
		}
	}

	// Delete tools that are no longer present on the server.
	for _, name := range diff.Removed {
		if err := r.queries.DeleteMCPToolByServerAndName(ctx, db.DeleteMCPToolByServerAndNameParams{
			ServerID: serverID,
			Name:     name,
		}); err != nil {
			return ToolDiff{}, fmt.Errorf("delete removed tool %q: %w", name, err)
		}
	}

	if err := r.queries.UpdateMCPServerLastDiscovered(ctx, db.UpdateMCPServerLastDiscoveredParams{
		LastDiscoveredAt: &now,
		ID:               serverID,
	}); err != nil {
		return ToolDiff{}, fmt.Errorf("update last_discovered_at: %w", err)
	}

	hasDrift := int64(0)
	isFirstDiscovery := len(oldTools) == 0
	if !isFirstDiscovery && (len(diff.Added) > 0 || len(diff.Removed) > 0 || len(diff.Modified) > 0) {
		hasDrift = 1
	}
	if err := r.queries.UpdateMCPServerDrift(ctx, db.UpdateMCPServerDriftParams{
		HasDrift: hasDrift,
		ID:       serverID,
	}); err != nil {
		return ToolDiff{}, fmt.Errorf("update has_drift: %w", err)
	}

	return diff, nil
}

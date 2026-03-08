package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// ResolvedTool pairs a granted tool's model metadata with a ready Client
// targeting its server. Used by the agent runner to call tools.
type ResolvedTool struct {
	model.GrantedTool
	Client *Client
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
	db *sql.DB
}

// NewRegistry returns a Registry backed by the given database connection.
func NewRegistry(db *sql.DB) *Registry {
	return &Registry{db: db}
}

// ResolveForPolicy resolves the granted tool list for a parsed policy,
// returning a ResolvedTool for each entry in capabilities.sensors and
// capabilities.actuators. Returns an error if any tool reference is not
// found in the DB — this is the fail-fast check at run start.
func (r *Registry) ResolveForPolicy(ctx context.Context, p *model.ParsedPolicy) ([]ResolvedTool, error) {
	// TODO: for each sensor and actuator in p.Capabilities, look up the
	// server and tool by dot-notation name, construct a Client, and return
	// the resolved list. Fail fast if any tool is missing.
	panic("not implemented")
}

// RegisterServer stores a new MCP server record, discovers its tools via the
// MCP client, and upserts all discovered tools into mcp_tools with
// capability_role defaulting to 'sensor'. last_discovered_at is intentionally
// left NULL here — it is set only by RefreshTools.
func (r *Registry) RegisterServer(ctx context.Context, name, url string) error {
	serverID := model.NewULID()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	q := db.New(r.db)

	if _, err := q.CreateMCPServer(ctx, db.CreateMCPServerParams{
		ID:        serverID,
		Name:      name,
		Url:       url,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("create mcp server: %w", err)
	}

	tools, err := NewClient(url).DiscoverTools(ctx)
	if err != nil {
		return fmt.Errorf("discover tools for server %q: %w", name, err)
	}

	for _, t := range tools {
		if _, err := q.UpsertMCPTool(ctx, db.UpsertMCPToolParams{
			ID:             model.NewULID(),
			ServerID:       serverID,
			Name:           t.Name,
			Description:    t.Description,
			InputSchema:    string(t.InputSchema),
			CapabilityRole: string(model.CapabilityRoleSensor),
			CreatedAt:      now,
		}); err != nil {
			return fmt.Errorf("upsert tool %q: %w", t.Name, err)
		}
	}

	return nil
}

// RefreshTools re-discovers tools for a registered server, computes the diff
// against the current DB state, upserts all fresh tools (preserving any
// operator-assigned capability_role for existing tools), deletes tools that
// have disappeared, and updates last_discovered_at.
//
// Selective per-name deletes are used rather than bulk DeleteMCPToolsByServer
// so that operator-assigned capability roles on unchanged tools are preserved.
func (r *Registry) RefreshTools(ctx context.Context, serverID string) (ToolDiff, error) {
	q := db.New(r.db)

	// Fetch current tool state from DB so we can compute the diff and
	// preserve existing capability_role values for unchanged tools.
	oldTools, err := q.ListMCPToolsByServer(ctx, serverID)
	if err != nil {
		return ToolDiff{}, fmt.Errorf("list existing tools: %w", err)
	}

	oldByName := make(map[string]db.McpTool, len(oldTools))
	for _, t := range oldTools {
		oldByName[t.Name] = t
	}

	srv, err := q.GetMCPServer(ctx, serverID)
	if err != nil {
		return ToolDiff{}, fmt.Errorf("get mcp server %q: %w", serverID, err)
	}

	freshTools, err := NewClient(srv.Url).DiscoverTools(ctx)
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
		// Modified means description or input_schema changed — capability_role
		// changes are not modifications in ToolDiff (they are operator-controlled).
		if old.Description != fresh.Description || old.InputSchema != string(fresh.InputSchema) {
			diff.Modified = append(diff.Modified, name)
		}
	}

	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Modified)

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Upsert all fresh tools. Preserve the existing capability_role for tools
	// already known to the DB so operator overrides (e.g. actuator) survive
	// re-discovery; default to 'sensor' for newly discovered tools.
	for _, t := range freshTools {
		role := string(model.CapabilityRoleSensor)
		if old, exists := oldByName[t.Name]; exists {
			role = old.CapabilityRole
		}

		toolID := model.NewULID()
		if old, exists := oldByName[t.Name]; exists {
			toolID = old.ID
		}

		if _, err := q.UpsertMCPTool(ctx, db.UpsertMCPToolParams{
			ID:             toolID,
			ServerID:       serverID,
			Name:           t.Name,
			Description:    t.Description,
			InputSchema:    string(t.InputSchema),
			CapabilityRole: role,
			CreatedAt:      now,
		}); err != nil {
			return ToolDiff{}, fmt.Errorf("upsert tool %q: %w", t.Name, err)
		}
	}

	// Delete tools that are no longer present on the server. Per-name deletes
	// (rather than bulk DeleteMCPToolsByServer) are used here so that any
	// operator-assigned capability roles on still-present tools survive. The
	// upsert above already handles those — only missing ones are deleted.
	for _, name := range diff.Removed {
		if _, err := r.db.ExecContext(ctx,
			`DELETE FROM mcp_tools WHERE server_id = ? AND name = ?`,
			serverID, name,
		); err != nil {
			return ToolDiff{}, fmt.Errorf("delete removed tool %q: %w", name, err)
		}
	}

	if err := q.UpdateMCPServerLastDiscovered(ctx, db.UpdateMCPServerLastDiscoveredParams{
		LastDiscoveredAt: &now,
		ID:               serverID,
	}); err != nil {
		return ToolDiff{}, fmt.Errorf("update last_discovered_at: %w", err)
	}

	return diff, nil
}

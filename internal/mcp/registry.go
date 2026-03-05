package mcp

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/rapp992/gleipnir/internal/model"
)

// ResolvedTool pairs a granted tool's model metadata with a ready Client
// targeting its server. Used by the agent runner to call tools.
type ResolvedTool struct {
	model.GrantedTool
	Client *Client
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
	_ = fmt.Errorf // prevent unused import during stub phase
	panic("not implemented")
}

// RegisterServer stores a new MCP server record, discovers its tools via the
// MCP client, and upserts all discovered tools into mcp_tools.
func (r *Registry) RegisterServer(ctx context.Context, name, url string) error {
	// TODO: generate ULID, insert mcp_servers row, call DiscoverTools,
	// upsert each tool into mcp_tools with capability_role defaulting to 'sensor'
	panic("not implemented")
}

// RefreshTools re-discovers tools for a registered server and upserts any
// changes. Removes tools that are no longer present on the server.
func (r *Registry) RefreshTools(ctx context.Context, serverID string) error {
	// TODO: call DiscoverTools, upsert new/changed tools, delete removed tools,
	// update mcp_servers.last_discovered_at
	panic("not implemented")
}

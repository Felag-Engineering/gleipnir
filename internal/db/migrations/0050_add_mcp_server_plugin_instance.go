package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddMCPServerPluginInstance adds mcp_servers.plugin_instance_id so a healthy
// managed-plugin generation can be an MCP server entry like any other
// (ADR-053, mcp-realignment-spec.md §3; issue #819).
//
// The point of the realignment is that there is ONE MCP client stack. A plugin
// stops being a thing Gleipnir talks to over a bespoke gRPC dispatcher and
// becomes a server it talks to over the same transport, through the same
// discovery, into the same tool namespace, with the same canonical-schema
// persistence. That only works if a managed plugin's endpoint is a row in this
// table — anything else reintroduces the second path the realignment exists to
// delete.
//
// This ONE column is the whole distinction, and the trust tier is DERIVED from
// it rather than stored beside it. A separate trust_tier column would be a
// second fact that must agree with the first, and two facts that must agree are
// two facts that can disagree — at which point a row could claim to be managed
// while pointing at no instance, or claim to be external while the reconciler
// rotates its URL underneath it. Deriving costs one branch and cannot drift.
//
// ON DELETE CASCADE, unlike plugin_audit_events.plugin_instance_id: an audit
// event is a record OF an instance and must outlive it, while this row is a
// route TO one. A route to a deleted instance is not history, it is a dangling
// endpoint the agent could still resolve a tool through.
//
// UNIQUE because the row is per INSTANCE, not per generation. A rotation
// updates the URL in place, which is also what makes the routing flip work:
// url is part of the registry cache's invalidation key, so a new generation's
// address evicts the cached client automatically, while a *Client already
// resolved into a running run keeps the old base URL and drains against the
// generation it started on.
type AddMCPServerPluginInstance struct{}

func (m *AddMCPServerPluginInstance) Version() int { return 40 }
func (m *AddMCPServerPluginInstance) Name() string { return "add_mcp_server_plugin_instance" }

func (m *AddMCPServerPluginInstance) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(mcp_servers)`)
	if err != nil {
		return false, fmt.Errorf("inspect mcp_servers columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan mcp_servers column: %w", err)
		}
		if name == "plugin_instance_id" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate mcp_servers columns: %w", err)
	}
	return false, nil
}

func (m *AddMCPServerPluginInstance) Up(ctx context.Context, tx *sql.Tx) error {
	// ADD COLUMN with a REFERENCES clause is legal in SQLite as long as the new
	// column defaults to NULL, which is what keeps this an in-place add. The
	// uniqueness is a partial index rather than a column constraint for the
	// same reason: ALTER TABLE ADD COLUMN cannot carry UNIQUE, and a partial
	// index is the better shape anyway — it leaves every external row's NULL
	// out of the index entirely instead of relying on SQLite treating NULLs as
	// distinct.
	const ddl = `
ALTER TABLE mcp_servers
ADD COLUMN plugin_instance_id TEXT REFERENCES plugin_instances(id) ON DELETE CASCADE;

CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_servers_plugin_instance
    ON mcp_servers(plugin_instance_id) WHERE plugin_instance_id IS NOT NULL;`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add mcp_servers.plugin_instance_id: %w", err)
	}

	slog.Info("migrated: added plugin_instance_id to mcp_servers")
	return nil
}

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddMCPServerProtocolVersion adds the protocol_version column to mcp_servers,
// which will pin the MCP protocol version negotiated with a registry entry at
// server/discover time (mcp-realignment-spec.md §11). NULL means "not yet
// probed" — nothing populates this column yet; that lands in a later issue.
//
// New deployments get the column from 0001_initial.sql; this migration is the
// upgrade path for existing databases.
type AddMCPServerProtocolVersion struct{}

func (m *AddMCPServerProtocolVersion) Version() int { return 32 }
func (m *AddMCPServerProtocolVersion) Name() string { return "add_mcp_server_protocol_version" }

func (m *AddMCPServerProtocolVersion) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int64
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('mcp_servers') WHERE name = 'protocol_version'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check protocol_version column: %w", err)
	}
	return count >= 1, nil
}

func (m *AddMCPServerProtocolVersion) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE mcp_servers ADD COLUMN protocol_version TEXT`,
	); err != nil {
		return fmt.Errorf("add protocol_version column: %w", err)
	}
	slog.Info("migrated: added mcp_servers.protocol_version")
	return nil
}

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddMCPServerInfo adds mcp_servers.server_name and server_version, the
// server's self-reported identity from server/discover (issue #772).
//
// Both columns are nullable: NULL means the server has not reported one (a
// legacy-era server, or never probed). The values are UNTRUSTED and are
// bounded to 128 bytes by the MCP client before they reach this table; no
// CHECK constraint here, matching the other mcp_servers columns.
type AddMCPServerInfo struct{}

func (m *AddMCPServerInfo) Version() int { return 46 }
func (m *AddMCPServerInfo) Name() string { return "add_mcp_server_info" }

func (m *AddMCPServerInfo) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
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
		// Both columns are added together in one transaction, so one is enough.
		if name == "server_name" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate mcp_servers columns: %w", err)
	}
	return false, nil
}

func (m *AddMCPServerInfo) Up(ctx context.Context, tx *sql.Tx) error {
	for _, ddl := range []string{
		`ALTER TABLE mcp_servers ADD COLUMN server_name TEXT;`,
		`ALTER TABLE mcp_servers ADD COLUMN server_version TEXT;`,
	} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("add mcp_servers server info column: %w", err)
		}
	}

	slog.Info("migrated: added server_name and server_version to mcp_servers")
	return nil
}

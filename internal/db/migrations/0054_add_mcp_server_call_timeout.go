package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddMCPServerCallTimeout adds mcp_servers.call_timeout_seconds, an optional
// per-server override of GLEIPNIR_MCP_TIMEOUT (issue #939). NULL means the
// server uses the instance default exactly as it did before this column
// existed.
//
// This is plaintext, like ca_cert_pem (#928) and unlike the ADR-039
// auth-header columns: a call timeout is not a secret, and an operator
// needs to be able to read back what they configured.
//
// Bounds (1..600 seconds) are enforced at the API layer, not with a CHECK
// constraint here -- the same choice #928 made for ca_cert_pem, keeping
// validation in one place.
type AddMCPServerCallTimeout struct{}

func (m *AddMCPServerCallTimeout) Version() int { return 44 }
func (m *AddMCPServerCallTimeout) Name() string { return "add_mcp_server_call_timeout" }

func (m *AddMCPServerCallTimeout) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
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
		if name == "call_timeout_seconds" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate mcp_servers columns: %w", err)
	}
	return false, nil
}

func (m *AddMCPServerCallTimeout) Up(ctx context.Context, tx *sql.Tx) error {
	const ddl = `ALTER TABLE mcp_servers ADD COLUMN call_timeout_seconds INTEGER;`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add mcp_servers.call_timeout_seconds: %w", err)
	}

	slog.Info("migrated: added call_timeout_seconds to mcp_servers")
	return nil
}

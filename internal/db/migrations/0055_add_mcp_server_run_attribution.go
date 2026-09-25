package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddMCPServerRunAttribution adds mcp_servers.run_attribution, an optional
// per-server setting that asserts who/what a tools/call is on behalf of
// (issue #943). NULL means off: nothing extra is sent, exactly as before
// this column existed.
//
// The column holds plaintext JSON, like ca_cert_pem (#928) and
// call_timeout_seconds (#939): header names are not secrets and an operator
// needs to read back what they configured. Validation (header-name rules,
// mode semantics) lives entirely at the API layer, not a CHECK constraint
// here, matching #928 and #939's precedent of keeping validation in one
// place.
type AddMCPServerRunAttribution struct{}

func (m *AddMCPServerRunAttribution) Version() int { return 45 }
func (m *AddMCPServerRunAttribution) Name() string { return "add_mcp_server_run_attribution" }

func (m *AddMCPServerRunAttribution) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
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
		if name == "run_attribution" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate mcp_servers columns: %w", err)
	}
	return false, nil
}

func (m *AddMCPServerRunAttribution) Up(ctx context.Context, tx *sql.Tx) error {
	const ddl = `ALTER TABLE mcp_servers ADD COLUMN run_attribution TEXT;`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add mcp_servers.run_attribution: %w", err)
	}

	slog.Info("migrated: added run_attribution to mcp_servers")
	return nil
}

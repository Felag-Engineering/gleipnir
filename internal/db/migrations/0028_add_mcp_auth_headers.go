package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AddMCPAuthHeaders adds the auth_headers_encrypted column to the mcp_servers
// table on existing deployments. New deployments get it from 0001_initial.sql;
// this migration is a no-op for them (ShouldSkip returns true when the column
// is already present).
type AddMCPAuthHeaders struct{}

func (m *AddMCPAuthHeaders) Version() int { return 18 }
func (m *AddMCPAuthHeaders) Name() string { return "add_mcp_auth_headers" }

func (m *AddMCPAuthHeaders) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(mcp_servers)`)
	if err != nil {
		return false, fmt.Errorf("pragma table_info(mcp_servers): %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("scan table_info row: %w", err)
		}
		if strings.EqualFold(name, "auth_headers_encrypted") {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (m *AddMCPAuthHeaders) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE mcp_servers ADD COLUMN auth_headers_encrypted TEXT;`,
	); err != nil {
		return fmt.Errorf("add auth_headers_encrypted column: %w", err)
	}
	slog.Info("migrated: added mcp_servers.auth_headers_encrypted")
	return nil
}

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddMCPToolEnabled adds the enabled column to the mcp_tools table on existing
// deployments. New deployments get it from 0001_initial.sql; this migration is
// a no-op for them. SQLite supports ALTER TABLE ADD COLUMN for simple NOT NULL
// DEFAULT columns without needing table recreation.
//
// The enabled flag is operator-managed: discovery never resets it, so a tool
// disabled by an operator stays disabled after re-discovery runs.
type AddMCPToolEnabled struct{}

func (m *AddMCPToolEnabled) Version() int { return 17 }
func (m *AddMCPToolEnabled) Name() string { return "add_mcp_tool_enabled" }

func (m *AddMCPToolEnabled) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(mcp_tools)")
	if err != nil {
		return false, fmt.Errorf("pragma table_info mcp_tools: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "enabled" {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (m *AddMCPToolEnabled) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE mcp_tools ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`); err != nil {
		return fmt.Errorf("alter table mcp_tools add enabled: %w", err)
	}

	slog.Info("migrated mcp_tools table to add enabled column")
	return nil
}

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// DropCapabilityRole drops the capability_role column from mcp_tools on
// existing deployments where it was present. New deployments get the schema
// without it from 0001_initial.sql. The tool/feedback distinction is now
// handled by the Gleipnir runtime as a native feedback primitive (ADR-031).
//
// SQLite does not support DROP COLUMN in older versions, so we use the
// table-recreation pattern. PRAGMA foreign_keys must be set OUTSIDE the
// transaction (SQLite requirement).
type DropCapabilityRole struct{}

func (m *DropCapabilityRole) Version() int { return 6 }
func (m *DropCapabilityRole) Name() string { return "drop_capability_role" }

func (m *DropCapabilityRole) RequiresForeignKeysOff() bool { return true }

func (m *DropCapabilityRole) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var tableSQL string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='mcp_tools'`,
	).Scan(&tableSQL)
	if err != nil {
		return false, fmt.Errorf("query mcp_tools schema: %w", err)
	}
	// If capability_role is absent the column was already dropped.
	return !strings.Contains(tableSQL, "capability_role"), nil
}

func (m *DropCapabilityRole) Up(ctx context.Context, tx *sql.Tx) error {
	// The INSERT INTO schema_migrations(version=10) is preserved verbatim from
	// the original store.go inline migration — behavior-preserving refactor only.
	ddl := `
CREATE TABLE mcp_tools_new (
    id              TEXT    PRIMARY KEY,
    server_id       TEXT    NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    description     TEXT    NOT NULL,
    input_schema    TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,
    UNIQUE(server_id, name)
);
INSERT INTO mcp_tools_new (id, server_id, name, description, input_schema, created_at)
    SELECT id, server_id, name, description, input_schema, created_at FROM mcp_tools;
DROP TABLE mcp_tools;
ALTER TABLE mcp_tools_new RENAME TO mcp_tools;
CREATE INDEX idx_mcp_tools_server_id ON mcp_tools(server_id);
INSERT INTO schema_migrations(version, applied_at) VALUES (10, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("recreate mcp_tools without capability_role: %w", err)
	}

	slog.Info("migrated mcp_tools table to drop capability_role")
	return nil
}

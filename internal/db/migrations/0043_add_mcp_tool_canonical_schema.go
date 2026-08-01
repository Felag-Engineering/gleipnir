package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddMCPToolCanonicalSchema adds the canonical_schema column to mcp_tools,
// which will hold the schemanorm-normalized form of input_schema (ADR-059 /
// mcp-realignment-spec.md §10 step 1) alongside the raw bytes MCP returned.
// NULL means "no canonical form stored" — either the row predates this
// column, or normalization failed and was logged (fail-open at discovery).
//
// Rows are NOT backfilled by this migration: SQLite cannot run schemanorm,
// and a Go-side backfill is out of scope. The next successful discovery
// (RefreshTools or the server-create probe) populates canonical_schema for
// every tool it upserts.
//
// New deployments get the column from 0001_initial.sql; this migration is the
// upgrade path for existing databases.
type AddMCPToolCanonicalSchema struct{}

func (m *AddMCPToolCanonicalSchema) Version() int { return 33 }
func (m *AddMCPToolCanonicalSchema) Name() string { return "add_mcp_tool_canonical_schema" }

func (m *AddMCPToolCanonicalSchema) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int64
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('mcp_tools') WHERE name = 'canonical_schema'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check canonical_schema column: %w", err)
	}
	return count >= 1, nil
}

func (m *AddMCPToolCanonicalSchema) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE mcp_tools ADD COLUMN canonical_schema TEXT`,
	); err != nil {
		return fmt.Errorf("add canonical_schema column: %w", err)
	}
	slog.Info("migrated: added mcp_tools.canonical_schema")
	return nil
}

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// MCPTasksInternalServer makes mcp_tasks.server_id nullable so the in-app
// channel can open a task without an MCP server behind it (ADR-055, spec §6.4;
// issue #801).
//
// The point of the change is that every Request gets ONE shape regardless of
// route. A plugin-routed Request is a Tasks-extension task on some MCP server;
// an in-app Request is the same lifecycle — create, poll, complete, cancel —
// with no MCP hop at all. Modelling the second as something other than a task
// would mean two resolution paths, two audit shapes, and two restart stories
// for what an operator experiences as one thing.
//
// NULL server_id is therefore not a missing value, it is a meaningful one:
// "resolved internally, no server involved". The alternative — a sentinel
// mcp_servers row for the in-app channel — would put a fake entry in the server
// registry that operators see, can edit, and can delete out from under a live
// task.
//
// The UNIQUE(server_id, task_id) constraint survives and keeps working:
// SQLite treats NULLs as distinct in a UNIQUE index, so internal tasks do not
// collide with each other on a shared NULL. Their task_id is a ULID the host
// mints, which is unique on its own.
type MCPTasksInternalServer struct{}

func (m *MCPTasksInternalServer) Version() int { return 38 }
func (m *MCPTasksInternalServer) Name() string { return "mcp_tasks_internal_server" }

// RequiresForeignKeysOff is required by the table-recreation pattern: SQLite
// needs PRAGMA foreign_keys set OUTSIDE the transaction.
func (m *MCPTasksInternalServer) RequiresForeignKeysOff() bool { return true }

func (m *MCPTasksInternalServer) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var tableSQL string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='mcp_tasks'`,
	).Scan(&tableSQL)
	if err != nil {
		return false, fmt.Errorf("query mcp_tasks schema: %w", err)
	}
	// Already rebuilt when server_id no longer carries NOT NULL. Matching on
	// the exact column line rather than the substring "NOT NULL" — the table
	// has several other NOT NULL columns.
	return !strings.Contains(normalizeWhitespace(tableSQL), "server_id TEXT NOT NULL"), nil
}

func (m *MCPTasksInternalServer) Up(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE mcp_tasks_new (
    id                TEXT    PRIMARY KEY,
    run_id            TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    server_id         TEXT    REFERENCES mcp_servers(id) ON DELETE CASCADE,
    task_id           TEXT    NOT NULL,
    kind              TEXT    NOT NULL CHECK(kind IN ('tool_call', 'channel_request')),
    poll_interval_ms  INTEGER,
    server_ttl        TEXT,
    status            TEXT    NOT NULL CHECK(status IN ('working', 'input_required', 'complete', 'failed', 'cancelled', 'expired')),
    result            TEXT,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL,
    UNIQUE(server_id, task_id)
);

INSERT INTO mcp_tasks_new
    (id, run_id, server_id, task_id, kind, poll_interval_ms, server_ttl, status, result, created_at, updated_at)
SELECT id, run_id, server_id, task_id, kind, poll_interval_ms, server_ttl, status, result, created_at, updated_at
FROM mcp_tasks;

DROP TABLE mcp_tasks;
ALTER TABLE mcp_tasks_new RENAME TO mcp_tasks;

CREATE INDEX idx_mcp_tasks_run_id ON mcp_tasks(run_id);
CREATE INDEX idx_mcp_tasks_status ON mcp_tasks(status);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("rebuild mcp_tasks with a nullable server_id: %w", err)
	}

	slog.Info("migrated: mcp_tasks.server_id is now nullable for internal (in-app) tasks")
	return nil
}

// normalizeWhitespace collapses runs of whitespace so a schema check does not
// depend on how the original DDL was formatted.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

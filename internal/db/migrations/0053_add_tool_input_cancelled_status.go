package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AddToolInputCancelledStatus adds 'cancelled' to the
// tool_input_requests.status CHECK constraint (ADR-061, mcp-realignment-spec
// §6.7 correction).
//
// Before this migration, a run cancelled while paused on a tool-initiated
// input request left the row 'pending' forever -- nothing ever transitioned
// it, so the timeout scanner would eventually (and wrongly) claim it as
// 'timed_out'. That mislabels a cancellation as an unanswered wait: an
// operator reading the row afterwards would conclude nobody was watching,
// when in fact the run was deliberately stopped.
//
// SQLite cannot ALTER a CHECK constraint in-place, so this migration rebuilds
// the table: create tool_input_requests_new with the extended CHECK list,
// copy all rows, drop the old table, rename, and re-create every index.
type AddToolInputCancelledStatus struct{}

func (m *AddToolInputCancelledStatus) Version() int { return 43 }
func (m *AddToolInputCancelledStatus) Name() string {
	return "add_tool_input_cancelled_status"
}

func (m *AddToolInputCancelledStatus) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var ddl string
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type='table' AND name='tool_input_requests'`,
	).Scan(&ddl)
	if err != nil {
		return false, fmt.Errorf("read tool_input_requests DDL: %w", err)
	}
	// A fresh install already ran 0001_initial.sql with the updated CHECK, or
	// the table does not exist yet for some other reason -- either way there
	// is nothing to migrate.
	if ddl == "" {
		return true, nil
	}
	return strings.Contains(ddl, "cancelled"), nil
}

// RequiresForeignKeysOff opts into the runner's PRAGMA foreign_keys=OFF
// toggle. tool_input_requests holds FK references to runs and mcp_servers;
// the DROP + RENAME rebuild requires FK enforcement off for the transaction
// (issue #494's precedent).
func (m *AddToolInputCancelledStatus) RequiresForeignKeysOff() bool { return true }

func (m *AddToolInputCancelledStatus) Up(ctx context.Context, tx *sql.Tx) error {
	// 1. Create the replacement table with the extended CHECK list. Column
	// order matches 0044 (base columns) + 0046 (deadline_source) + 0047
	// (replay_context) -- the same order 0001_initial.sql ships for a fresh
	// install.
	const ddl = `
CREATE TABLE tool_input_requests_new (
    id                TEXT    PRIMARY KEY,
    run_id            TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    server_id         TEXT    NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    tool_name         TEXT    NOT NULL,
    call_args         TEXT    NOT NULL,
    request_state     TEXT    NOT NULL,
    request_payload   TEXT    NOT NULL,
    elicitation_kind  TEXT    NOT NULL CHECK(elicitation_kind IN ('permission', 'information')),
    status            TEXT    NOT NULL CHECK(status IN ('pending', 'resolved', 'timed_out', 'cancelled')),
    response          TEXT,
    resolved_at       TEXT,
    expires_at        TEXT    NOT NULL,
    deadline_source   TEXT    CHECK(deadline_source IS NULL OR deadline_source IN ('policy', 'server_ttl', 'request_state')),
    replay_context    TEXT,
    created_at        TEXT    NOT NULL
);

INSERT INTO tool_input_requests_new (
    id, run_id, server_id, tool_name, call_args, request_state, request_payload,
    elicitation_kind, status, response, resolved_at, expires_at, deadline_source,
    replay_context, created_at
)
SELECT
    id, run_id, server_id, tool_name, call_args, request_state, request_payload,
    elicitation_kind, status, response, resolved_at, expires_at, deadline_source,
    replay_context, created_at
FROM tool_input_requests;

DROP TABLE tool_input_requests;
ALTER TABLE tool_input_requests_new RENAME TO tool_input_requests;

CREATE INDEX idx_tool_input_requests_run_id         ON tool_input_requests(run_id);
CREATE INDEX idx_tool_input_requests_run_pending    ON tool_input_requests(run_id, status);
CREATE INDEX idx_tool_input_requests_status_expires ON tool_input_requests(status, expires_at);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add 'cancelled' to tool_input_requests.status CHECK constraint: %w", err)
	}

	slog.Info("migrated: added cancelled to tool_input_requests status CHECK constraint")
	return nil
}

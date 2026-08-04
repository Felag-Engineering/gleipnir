package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddToolInputRequestsAndMCPTasks creates the tool_input_requests and mcp_tasks
// tables on existing deployments. New deployments get them from
// 0001_initial.sql; this migration is the upgrade path for existing databases.
//
// tool_input_requests persists tool-initiated human-in-the-loop waits (ADR-055,
// mcp-realignment-spec.md §6): a durable record of the original tools/call
// (server, tool, args), the opaque MRTR requestState the server returned with
// its input_required result, the elicitation-shaped request payload, and the
// operator's eventual response. This is what lets a human answer be applied
// after a host restart.
//
// mcp_tasks persists MCP Tasks-extension handles (SEP-2663) so polling can
// resume after a restart -- the §13 durability claim ("persisted
// requestState/task handles + channel-Request task re-polling survive
// restarts; runs do not"). The kind column deliberately covers both tool_call
// and channel_request so the same table backs the eventual §6.4
// Channel-Request-as-task path, not just tool-initiated waits.
//
// See schemas/sql_schemas.sql for the canonical column-by-column reference.
type AddToolInputRequestsAndMCPTasks struct{}

func (m *AddToolInputRequestsAndMCPTasks) Version() int { return 34 }
func (m *AddToolInputRequestsAndMCPTasks) Name() string {
	return "add_tool_input_requests_and_mcp_tasks"
}

func (m *AddToolInputRequestsAndMCPTasks) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	// Probe the last table created by Up. If it exists, tool_input_requests
	// also exists -- they ship as a single unit.
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mcp_tasks'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check mcp_tasks existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddToolInputRequestsAndMCPTasks) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE tool_input_requests (
    id                TEXT    PRIMARY KEY,                                              -- ULID
    run_id            TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    server_id         TEXT    NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,     -- server that owns the original tools/call
    tool_name         TEXT    NOT NULL,                                                 -- original tools/call name
    call_args         TEXT    NOT NULL,                                                 -- JSON blob, original tools/call arguments
    request_state     TEXT    NOT NULL,                                                 -- opaque MRTR requestState from the server's InputRequiredResult; size-capped at write (defense in depth, application layer, spec sec 6.2)
    request_payload   TEXT    NOT NULL,                                                 -- JSON blob: elicitation-shaped payload (messages + inputRequests/requestedSchema)
    elicitation_kind  TEXT    NOT NULL CHECK(elicitation_kind IN ('permission', 'information')),  -- spec sec 6.1
    status            TEXT    NOT NULL CHECK(status IN ('pending', 'resolved', 'timed_out')),
    response          TEXT,                                                             -- nullable, JSON blob of inputResponses / operator answer
    resolved_at       TEXT,                                                             -- nullable, ISO 8601 UTC
    expires_at        TEXT    NOT NULL,                                                 -- effective deadline: min of Gleipnir policy timeout / server TTL / requestState TTL (spec sec 6.3)
    created_at        TEXT    NOT NULL                                                  -- ISO 8601 UTC
);
CREATE INDEX idx_tool_input_requests_run_id         ON tool_input_requests(run_id);
CREATE INDEX idx_tool_input_requests_run_pending    ON tool_input_requests(run_id, status);
CREATE INDEX idx_tool_input_requests_status_expires ON tool_input_requests(status, expires_at);

CREATE TABLE mcp_tasks (
    id                TEXT    PRIMARY KEY,                                              -- ULID
    run_id            TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    server_id         TEXT    NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,     -- server hosting the durable task
    task_id           TEXT    NOT NULL,                                                 -- server-assigned Tasks-extension task id (SEP-2663)
    kind              TEXT    NOT NULL CHECK(kind IN ('tool_call', 'channel_request')),  -- reused by both the tool-initiated wait path and the eventual channel-Request-as-task path (spec sec 6.4)
    poll_interval_ms  INTEGER,                                                          -- nullable; server-suggested poll cadence
    server_ttl        TEXT,                                                             -- nullable, ISO 8601 UTC; server-side task expiry -- "weather", not authoritative (spec sec 6.3)
    status            TEXT    NOT NULL CHECK(status IN ('working', 'input_required', 'complete', 'failed', 'cancelled', 'expired')),
    result            TEXT,                                                             -- nullable, JSON blob; terminal task result
    created_at        TEXT    NOT NULL,                                                 -- ISO 8601 UTC
    updated_at        TEXT    NOT NULL,                                                 -- ISO 8601 UTC
    UNIQUE(server_id, task_id)
);
CREATE INDEX idx_mcp_tasks_run_id ON mcp_tasks(run_id);
CREATE INDEX idx_mcp_tasks_status ON mcp_tasks(status);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create tool_input_requests and mcp_tasks tables: %w", err)
	}

	slog.Info("migrated: created tool_input_requests and mcp_tasks tables")
	return nil
}

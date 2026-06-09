package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddPluginAudiencesAndPendingRequests creates the plugin_audiences,
// audience_entries, and plugin_pending_requests tables on existing deployments.
// New deployments get them from 0001_initial.sql; this migration is a no-op for them.
//
// See docs/developer/plugin-system-spec.md §6.1 (audience structure),
// §4.2 (Channel model / Request lifecycle), and §11.5 (uninstall semantics).
type AddPluginAudiencesAndPendingRequests struct{}

func (m *AddPluginAudiencesAndPendingRequests) Version() int { return 20 }
func (m *AddPluginAudiencesAndPendingRequests) Name() string {
	return "add_plugin_audiences_and_pending_requests"
}

func (m *AddPluginAudiencesAndPendingRequests) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	// Probe the last table created by Up. If it exists, every preceding
	// table from this migration also exists — they ship as a single unit.
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_pending_requests'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check plugin_pending_requests existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddPluginAudiencesAndPendingRequests) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE plugin_audiences (
    id                  TEXT    PRIMARY KEY,                                 -- ULID (ADR-013)
    name                TEXT    NOT NULL UNIQUE,
    created_by_user_id  TEXT    REFERENCES users(id) ON DELETE SET NULL,
    version             INTEGER NOT NULL DEFAULT 0,                          -- ADR-038 CAS counter
    created_at          TEXT    NOT NULL,                                    -- ISO 8601 UTC
    updated_at          TEXT    NOT NULL                                     -- ISO 8601 UTC
);

CREATE TABLE audience_entries (
    id                 TEXT    PRIMARY KEY,                                  -- ULID
    audience_id        TEXT    NOT NULL REFERENCES plugin_audiences(id) ON DELETE CASCADE,
    plugin_instance_id TEXT    NOT NULL REFERENCES plugin_instances(id) ON DELETE RESTRICT,
    position           INTEGER NOT NULL,
    notify             INTEGER NOT NULL DEFAULT 0,
    request            INTEGER NOT NULL DEFAULT 0,
    config_json        TEXT    NOT NULL DEFAULT '{}',
    UNIQUE (audience_id, position)
);
CREATE INDEX idx_audience_entries_audience  ON audience_entries(audience_id);
CREATE INDEX idx_audience_entries_instance  ON audience_entries(plugin_instance_id);

CREATE TABLE plugin_pending_requests (
    id                  TEXT    PRIMARY KEY,                                 -- ULID; spec's request_id
    plugin_instance_id  TEXT    NOT NULL REFERENCES plugin_instances(id) ON DELETE RESTRICT,
    run_id              TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    audience_entry_id   TEXT    REFERENCES audience_entries(id) ON DELETE SET NULL,
    tool_name           TEXT    NOT NULL DEFAULT '',
    status              TEXT    NOT NULL CHECK(status IN ('pending','resolved','timed_out')),
    response            TEXT,
    expires_at          TEXT,
    resolved_at         TEXT,
    created_at          TEXT    NOT NULL
);
CREATE INDEX idx_plugin_pending_requests_run_status      ON plugin_pending_requests(run_id, status);
CREATE INDEX idx_plugin_pending_requests_status_expires  ON plugin_pending_requests(status, expires_at);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create plugin audiences and pending requests tables: %w", err)
	}

	slog.Info("migrated: created plugin_audiences, audience_entries, plugin_pending_requests tables")
	return nil
}

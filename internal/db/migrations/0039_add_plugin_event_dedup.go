package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddPluginEventDedup creates the plugin_event_dedup table on existing
// deployments. New deployments get it from 0001_initial.sql; this migration
// is a no-op for them (ShouldSkip detects the existing table).
//
// The table provides a rolling-window dedup store for at-least-once plugin
// substrate events (spec §4.3, issue #562). Events are keyed by
// (plugin_instance_id, event_kind, event_id); a host-assigned created_at_ms
// column (Unix milliseconds) drives TTL eviction rather than the
// plugin-supplied event_id, which is not guaranteed to be time-sortable.
type AddPluginEventDedup struct{}

func (m *AddPluginEventDedup) Version() int { return 29 }
func (m *AddPluginEventDedup) Name() string { return "add_plugin_event_dedup" }

func (m *AddPluginEventDedup) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_event_dedup'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check plugin_event_dedup existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddPluginEventDedup) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE plugin_event_dedup (
    plugin_instance_id TEXT    NOT NULL REFERENCES plugin_instances(id) ON DELETE CASCADE,
    event_kind         TEXT    NOT NULL,
    event_id           TEXT    NOT NULL,
    created_at_ms      INTEGER NOT NULL,
    PRIMARY KEY (plugin_instance_id, event_kind, event_id)
) WITHOUT ROWID;
CREATE INDEX idx_plugin_event_dedup_created_at_ms ON plugin_event_dedup(created_at_ms)`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create plugin_event_dedup: %w", err)
	}

	slog.Info("migrated: created plugin_event_dedup table")
	return nil
}

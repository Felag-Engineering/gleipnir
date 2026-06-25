package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddHostEventRateLimit adds the host_event_rate_per_sec and host_event_burst
// columns to plugin_instances. These are host-owned columns (outside config_json)
// that let an operator override the per-instance EmitEvent rate limit enforced by
// the host-side ingress (spec §4.3, issue #577).
//
// Columns are nullable with no DEFAULT: existing/new rows start NULL, which causes
// the limiter to fall back to the hardcoded defaults (100 events/sec, burst 200).
// This is a scoped ADR-002 deviation identical to credentials_encrypted: host
// self-protection data that must never travel inside the plugin-author-controlled
// config_json blob.
type AddHostEventRateLimit struct{}

func (m *AddHostEventRateLimit) Version() int { return 30 }
func (m *AddHostEventRateLimit) Name() string { return "add_host_event_rate_limit" }

func (m *AddHostEventRateLimit) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int64
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('plugin_instances')
		 WHERE name IN ('host_event_rate_per_sec', 'host_event_burst')`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check host_event_rate_limit columns: %w", err)
	}
	// Skip only when BOTH columns are already present.
	return count >= 2, nil
}

func (m *AddHostEventRateLimit) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE plugin_instances ADD COLUMN host_event_rate_per_sec REAL`,
	); err != nil {
		return fmt.Errorf("add host_event_rate_per_sec column: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE plugin_instances ADD COLUMN host_event_burst INTEGER`,
	); err != nil {
		return fmt.Errorf("add host_event_burst column: %w", err)
	}
	slog.Info("migrated: added host_event_rate_per_sec and host_event_burst to plugin_instances")
	return nil
}

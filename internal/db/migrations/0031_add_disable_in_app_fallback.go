package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddDisableInAppFallback adds the disable_in_app_fallback column to
// plugin_audiences. New deployments get it from 0001_initial.sql; this
// migration is a no-op for them. See issue #208 and plugin-system-spec §6.2.
type AddDisableInAppFallback struct{}

func (m *AddDisableInAppFallback) Version() int { return 21 }
func (m *AddDisableInAppFallback) Name() string { return "add_disable_in_app_fallback" }

func (m *AddDisableInAppFallback) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	// pragma_table_info returns one row per column; zero rows means the column
	// doesn't exist yet.
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('plugin_audiences') WHERE name='disable_in_app_fallback'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check disable_in_app_fallback column: %w", err)
	}
	return count > 0, nil
}

func (m *AddDisableInAppFallback) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `ALTER TABLE plugin_audiences ADD COLUMN disable_in_app_fallback INTEGER NOT NULL DEFAULT 0;`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add disable_in_app_fallback to plugin_audiences: %w", err)
	}
	slog.Info("migrated: added disable_in_app_fallback column to plugin_audiences")
	return nil
}

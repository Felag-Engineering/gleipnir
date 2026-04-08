package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddSystemAndModelSettings creates the system_settings and model_settings
// tables on existing deployments. New deployments get both from
// 0001_initial.sql; this migration is a no-op for them.
type AddSystemAndModelSettings struct{}

func (m *AddSystemAndModelSettings) Version() int { return 11 }
func (m *AddSystemAndModelSettings) Name() string { return "add_system_and_model_settings" }

func (m *AddSystemAndModelSettings) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='system_settings'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check system_settings existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddSystemAndModelSettings) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE system_settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE model_settings (
    provider    TEXT    NOT NULL,
    model_name  TEXT    NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    updated_at  TEXT    NOT NULL,
    PRIMARY KEY (provider, model_name)
);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create system_settings and model_settings: %w", err)
	}

	slog.Info("migrated: created system_settings and model_settings tables")
	return nil
}

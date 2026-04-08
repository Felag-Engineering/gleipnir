package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddOpenAICompatProviders creates the openai_compat_providers table on
// existing deployments. New deployments get it from 0001_initial.sql; this
// migration is a no-op for them.
type AddOpenAICompatProviders struct{}

func (m *AddOpenAICompatProviders) Version() int { return 12 }
func (m *AddOpenAICompatProviders) Name() string { return "add_openai_compat_providers" }

func (m *AddOpenAICompatProviders) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='openai_compat_providers'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check openai_compat_providers existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddOpenAICompatProviders) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE openai_compat_providers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL UNIQUE,
    base_url          TEXT    NOT NULL,
    api_key_encrypted TEXT    NOT NULL,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);

CREATE INDEX idx_openai_compat_providers_name ON openai_compat_providers(name);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create openai_compat_providers: %w", err)
	}

	slog.Info("migrated: created openai_compat_providers table")
	return nil
}

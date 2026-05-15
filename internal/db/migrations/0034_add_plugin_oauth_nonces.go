package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddPluginOAuthNonces creates the plugin_oauth_nonces table used to enforce
// single-use CSRF nonces in the OAuth2 authorization code flow (issue #225).
// Nonces are persisted so they survive process restarts mid-dance.
type AddPluginOAuthNonces struct{}

func (m *AddPluginOAuthNonces) Version() int { return 24 }
func (m *AddPluginOAuthNonces) Name() string { return "add_plugin_oauth_nonces" }

func (m *AddPluginOAuthNonces) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_oauth_nonces'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check plugin_oauth_nonces existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddPluginOAuthNonces) Up(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE plugin_oauth_nonces (
    nonce       TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    created_at  TEXT NOT NULL
) STRICT`)
	if err != nil {
		return fmt.Errorf("create plugin_oauth_nonces table: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`CREATE INDEX plugin_oauth_nonces_expires_at_idx ON plugin_oauth_nonces (expires_at)`,
	)
	if err != nil {
		return fmt.Errorf("create plugin_oauth_nonces_expires_at_idx: %w", err)
	}
	slog.Info("created plugin_oauth_nonces table")
	return nil
}

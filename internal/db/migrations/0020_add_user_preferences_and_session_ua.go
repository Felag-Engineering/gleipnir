package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddUserPreferencesAndSessionUA creates the user_preferences table and adds
// user_agent/ip_address columns to sessions on existing deployments. New
// deployments get both from 0001_initial.sql; this migration is a no-op for them.
//
// This migration has two independent idempotency checks, each guarding its own
// DDL. No ShouldSkip is implemented — Up() is safe to re-enter because every
// step checks its own precondition. SQLite supports ALTER TABLE ADD COLUMN
// inside a transaction.
type AddUserPreferencesAndSessionUA struct{}

func (m *AddUserPreferencesAndSessionUA) Version() int { return 9 }
func (m *AddUserPreferencesAndSessionUA) Name() string {
	return "add_user_preferences_and_session_ua"
}

func (m *AddUserPreferencesAndSessionUA) Up(ctx context.Context, tx *sql.Tx) error {
	// Create user_preferences table if it doesn't exist yet.
	var prefCount int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user_preferences'`,
	).Scan(&prefCount)
	if err != nil {
		return fmt.Errorf("check user_preferences existence: %w", err)
	}
	if prefCount == 0 {
		ddl := `CREATE TABLE user_preferences (
    user_id          TEXT NOT NULL REFERENCES users(id),
    preference_key   TEXT NOT NULL,
    preference_value TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    UNIQUE(user_id, preference_key)
);`
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create user_preferences: %w", err)
		}
		slog.Info("migrated: created user_preferences table")
	}

	// Add user_agent and ip_address columns to sessions if they don't exist.
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(sessions)")
	if err != nil {
		return fmt.Errorf("pragma table_info sessions: %w", err)
	}
	defer rows.Close()

	var hasUserAgent, hasIPAddress bool
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "user_agent" {
			hasUserAgent = true
		}
		if name == "ip_address" {
			hasIPAddress = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pragma rows: %w", err)
	}

	if !hasUserAgent {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("alter table sessions add user_agent: %w", err)
		}
		slog.Info("migrated sessions table to add user_agent column")
	}
	if !hasIPAddress {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN ip_address TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("alter table sessions add ip_address: %w", err)
		}
		slog.Info("migrated sessions table to add ip_address column")
	}

	return nil
}

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddUserSlackUserID adds the slack_user_id column to the users table so that
// a Gleipnir user can be linked to a Slack workspace user. The mapping is
// explicit (admin-set) and one-to-one: at most one Gleipnir user per Slack id.
//
// SQLite does not support ADD COLUMN with an inline UNIQUE constraint, so the
// index is created separately after the ALTER TABLE.
//
// This is a scoped ADR-002 deviation identical to webhook_secret_encrypted:
// identity data that must never travel inside the policy YAML blob.
type AddUserSlackUserID struct{}

func (m *AddUserSlackUserID) Version() int { return 31 }
func (m *AddUserSlackUserID) Name() string { return "add_user_slack_user_id" }

func (m *AddUserSlackUserID) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int64
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name = 'slack_user_id'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check slack_user_id column: %w", err)
	}
	return count >= 1, nil
}

func (m *AddUserSlackUserID) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE users ADD COLUMN slack_user_id TEXT`,
	); err != nil {
		return fmt.Errorf("add slack_user_id column: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_slack_user_id ON users(slack_user_id)`,
	); err != nil {
		return fmt.Errorf("create idx_users_slack_user_id index: %w", err)
	}
	slog.Info("migrated: added slack_user_id to users table")
	return nil
}

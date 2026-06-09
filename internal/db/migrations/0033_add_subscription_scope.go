package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddSubscriptionScope adds the subscription_scope_json column to plugin_instances.
// The column stores the operator-configured coarse subscription scope sent to the
// plugin via TriggerService.Start as watch_scope_json (spec §4.3, issue #223).
//
// NOT encrypted — scope is operator-readable configuration, not a credential
// (mirrors config_json at schema line 413).
type AddSubscriptionScope struct{}

func (m *AddSubscriptionScope) Version() int { return 23 }
func (m *AddSubscriptionScope) Name() string { return "add_subscription_scope" }

func (m *AddSubscriptionScope) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int64
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('plugin_instances') WHERE name='subscription_scope_json'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check subscription_scope_json column: %w", err)
	}
	return count > 0, nil
}

func (m *AddSubscriptionScope) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE plugin_instances ADD COLUMN subscription_scope_json TEXT NOT NULL DEFAULT '{}'`,
	); err != nil {
		return fmt.Errorf("add subscription_scope_json column: %w", err)
	}
	slog.Info("added subscription_scope_json column to plugin_instances")
	return nil
}

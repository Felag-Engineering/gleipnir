package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddPluginUserIdentities creates plugin_user_identities on existing
// deployments. New deployments get it from 0001_initial.sql, and this
// migration skips for them.
//
// It maps a channel instance's opaque actor id to a Gleipnir user (ADR-058,
// spec §9.1), replacing the global, vendor-named users.slack_user_id column:
// two instances of one channel plugin may serve different workspaces, so an
// actor id only means something together with the instance that issued it.
// The keys say exactly that: one identity per user per instance, and one user
// per actor id per instance.
//
// link_method allows only 'admin_set' for now; the verified link methods of
// §9.1 widen the CHECK when they land. There is deliberately no backfill from
// users.slack_user_id (owner decision): that column was global, and copying
// it to every instance would grant a mapping no admin made for that instance.
type AddPluginUserIdentities struct{}

func (m *AddPluginUserIdentities) Version() int { return 51 }
func (m *AddPluginUserIdentities) Name() string { return "add_plugin_user_identities" }

func (m *AddPluginUserIdentities) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_user_identities'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check plugin_user_identities existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddPluginUserIdentities) Up(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE plugin_user_identities (
    instance_id      TEXT NOT NULL REFERENCES plugin_instances(id) ON DELETE CASCADE,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    external_user_id TEXT NOT NULL,
    link_method      TEXT NOT NULL CHECK(link_method IN ('admin_set')),
    verified_at      TEXT,
    created_at       TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_id),
    UNIQUE (instance_id, external_user_id)
);`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create plugin_user_identities: %w", err)
	}
	slog.Info("migrated: created plugin_user_identities table")
	return nil
}

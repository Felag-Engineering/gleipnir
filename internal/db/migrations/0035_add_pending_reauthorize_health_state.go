package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AddPendingReauthorizeHealthState adds 'pending_reauthorize' to the
// plugin_instances.health_state CHECK constraint (issue #230).
//
// SQLite cannot ALTER a CHECK constraint in-place, so this migration rebuilds
// the table: create plugin_instances_new with the extended CHECK list, copy all
// rows, drop the old table, rename, and re-create both indexes.
type AddPendingReauthorizeHealthState struct{}

func (m *AddPendingReauthorizeHealthState) Version() int { return 25 }
func (m *AddPendingReauthorizeHealthState) Name() string {
	return "add_pending_reauthorize_health_state"
}

func (m *AddPendingReauthorizeHealthState) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	// Check whether the current CHECK constraint already allows pending_reauthorize
	// by attempting a well-formed no-op query against the constraint list.
	// The most reliable way: look at the CREATE TABLE SQL stored in sqlite_master.
	var ddl string
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type='table' AND name='plugin_instances'`,
	).Scan(&ddl)
	if err != nil {
		return false, fmt.Errorf("read plugin_instances DDL: %w", err)
	}
	// If the table doesn't exist yet (fresh install ran 0001_initial.sql with the
	// updated CHECK), there is nothing to migrate.
	if ddl == "" {
		return true, nil
	}
	// If the DDL already contains pending_reauthorize the migration was applied.
	return strings.Contains(ddl, "pending_reauthorize"), nil
}

func (m *AddPendingReauthorizeHealthState) Up(ctx context.Context, tx *sql.Tx) error {
	// 1. Create the replacement table with the extended CHECK list.
	_, err := tx.ExecContext(ctx, `
CREATE TABLE plugin_instances_new (
    id                       TEXT    PRIMARY KEY,
    plugin_id                TEXT    NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
    instance_name            TEXT    NOT NULL,
    config_json              TEXT    NOT NULL DEFAULT '{}',
    subscription_scope_json  TEXT    NOT NULL DEFAULT '{}',
    credentials_encrypted    TEXT,
    credentials_expires_at   TEXT,
    handshake_versions       TEXT    NOT NULL DEFAULT '{}',
    health_state             TEXT    NOT NULL DEFAULT 'pending_key_approval'
                                     CHECK(health_state IN (
                                         'healthy',
                                         'signature_invalid',
                                         'pending_key_approval',
                                         'pending_manifest_approval',
                                         'pending_config_migration',
                                         'verification_error',
                                         'unsigned_permissive',
                                         'unhealthy',
                                         'crashed',
                                         'circuit_broken',
                                         'pending_reauthorize'
                                     )),
    health_detail            TEXT,
    last_oauth_callback_url  TEXT,
    version                  INTEGER NOT NULL DEFAULT 0,
    created_at               TEXT    NOT NULL,
    updated_at               TEXT    NOT NULL,
    UNIQUE (plugin_id, instance_name)
)`)
	if err != nil {
		return fmt.Errorf("create plugin_instances_new: %w", err)
	}

	// 2. Copy all existing rows. SELECT * works because both tables share the
	// same column list (the new table may have subscription_scope_json that
	// was added by an earlier migration; we rely on column order matching).
	_, err = tx.ExecContext(ctx, `
INSERT INTO plugin_instances_new
SELECT id, plugin_id, instance_name, config_json, subscription_scope_json,
       credentials_encrypted, credentials_expires_at, handshake_versions,
       health_state, health_detail, last_oauth_callback_url, version,
       created_at, updated_at
FROM plugin_instances`)
	if err != nil {
		return fmt.Errorf("copy plugin_instances rows: %w", err)
	}

	// 3. Drop the old table and rename the replacement.
	if _, err := tx.ExecContext(ctx, `DROP TABLE plugin_instances`); err != nil {
		return fmt.Errorf("drop old plugin_instances: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE plugin_instances_new RENAME TO plugin_instances`); err != nil {
		return fmt.Errorf("rename plugin_instances_new: %w", err)
	}

	// 4. Re-create both indexes.
	if _, err := tx.ExecContext(ctx, `CREATE INDEX idx_plugin_instances_plugin_id ON plugin_instances(plugin_id)`); err != nil {
		return fmt.Errorf("create idx_plugin_instances_plugin_id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX idx_plugin_instances_health_state ON plugin_instances(health_state)`); err != nil {
		return fmt.Errorf("create idx_plugin_instances_health_state: %w", err)
	}

	slog.Info("migrated: added pending_reauthorize to plugin_instances health_state CHECK constraint")
	return nil
}

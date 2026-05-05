package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddPluginTables creates the plugins, plugin_instances, and
// plugin_audit_events tables on existing deployments. New deployments get
// them from 0001_initial.sql; this migration is a no-op for them.
//
// See ADR-041 (plugin system umbrella), ADR-045 (signing & TOFU trust), and
// ADR-046 (audit-table split) for the design decisions backing each column.
type AddPluginTables struct{}

func (m *AddPluginTables) Version() int { return 19 }
func (m *AddPluginTables) Name() string { return "add_plugin_tables" }

func (m *AddPluginTables) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	// Probe the last table created by Up. If it exists, every preceding
	// table from this migration also exists — they ship as a single unit.
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_audit_events'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check plugin_audit_events existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddPluginTables) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE plugins (
    id                 TEXT    PRIMARY KEY,
    name               TEXT    NOT NULL UNIQUE,
    plugin_version     TEXT    NOT NULL,
    manifest_snapshot  TEXT    NOT NULL,
    trusted_pubkey     TEXT    NOT NULL,
    status             TEXT    NOT NULL CHECK(status IN ('pending_review','active','removed')),
    version            INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL
);
CREATE INDEX idx_plugins_status ON plugins(status);

CREATE TABLE plugin_instances (
    id                       TEXT    PRIMARY KEY,
    plugin_id                TEXT    NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
    instance_name            TEXT    NOT NULL,
    config_json              TEXT    NOT NULL DEFAULT '{}',
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
                                         'unsigned_permissive'
                                     )),
    health_detail            TEXT,
    last_oauth_callback_url  TEXT,
    version                  INTEGER NOT NULL DEFAULT 0,
    created_at               TEXT    NOT NULL,
    updated_at               TEXT    NOT NULL,
    UNIQUE (plugin_id, instance_name)
);
CREATE INDEX idx_plugin_instances_plugin_id    ON plugin_instances(plugin_id);
CREATE INDEX idx_plugin_instances_health_state ON plugin_instances(health_state);

CREATE TABLE plugin_audit_events (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    plugin_instance_id  TEXT    REFERENCES plugin_instances(id) ON DELETE SET NULL,
    event_type          TEXT    NOT NULL,
    severity            TEXT    NOT NULL CHECK(severity IN ('info','warning','high','critical')),
    actor_user_id       TEXT    REFERENCES users(id) ON DELETE SET NULL,
    payload_json        TEXT    NOT NULL,
    created_at          TEXT    NOT NULL
);
CREATE INDEX idx_pae_instance_created ON plugin_audit_events(plugin_instance_id, created_at);
CREATE INDEX idx_pae_event_created    ON plugin_audit_events(event_type, created_at);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create plugin tables: %w", err)
	}

	slog.Info("migrated: created plugins, plugin_instances, plugin_audit_events tables")
	return nil
}

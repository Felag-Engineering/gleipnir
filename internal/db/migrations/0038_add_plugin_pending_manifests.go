package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddPluginPendingManifests creates the plugin_pending_manifests table on
// existing deployments. New deployments get it from 0001_initial.sql; this
// migration is a no-op for them (ShouldSkip detects the existing table).
//
// The table replaces the audit-event scan in findCandidateManifestEvent with
// a single indexed point-read per plugin. See issue #362 for the full rationale.
type AddPluginPendingManifests struct{}

func (m *AddPluginPendingManifests) Version() int { return 28 }
func (m *AddPluginPendingManifests) Name() string { return "add_plugin_pending_manifests" }

func (m *AddPluginPendingManifests) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_pending_manifests'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check plugin_pending_manifests existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddPluginPendingManifests) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE plugin_pending_manifests (
    plugin_id          TEXT PRIMARY KEY REFERENCES plugins(id) ON DELETE CASCADE,
    candidate_manifest TEXT NOT NULL,
    old_version        TEXT NOT NULL,
    new_version        TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
)`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create plugin_pending_manifests: %w", err)
	}

	slog.Info("migrated: created plugin_pending_manifests table")
	return nil
}

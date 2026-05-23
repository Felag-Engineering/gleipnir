package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AddPluginBinaryPath adds the binary_path column to the plugins table on
// existing deployments. New deployments get it from the schema; this migration
// is a no-op for them (ShouldSkip returns true when the column is already present).
//
// The column is nullable so existing rows survive without a backfill — NULL means
// "no binary path persisted yet (legacy row)". Manager.StartAllActive skips
// instances whose plugin row has a NULL binary_path and logs a warning directing
// the operator to re-install the tarball (issue #386).
type AddPluginBinaryPath struct{}

func (m *AddPluginBinaryPath) Version() int { return 26 }
func (m *AddPluginBinaryPath) Name() string { return "add_plugin_binary_path" }

func (m *AddPluginBinaryPath) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(plugins)`)
	if err != nil {
		return false, fmt.Errorf("pragma table_info(plugins): %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("scan table_info row: %w", err)
		}
		if strings.EqualFold(name, "binary_path") {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (m *AddPluginBinaryPath) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE plugins ADD COLUMN binary_path TEXT;`,
	); err != nil {
		return fmt.Errorf("add binary_path column: %w", err)
	}
	slog.Info("migrated: added plugins.binary_path")
	return nil
}

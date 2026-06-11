package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddRunsVersion adds the version column to the runs table for optimistic locking.
// Every status-changing UPDATE increments version; callers supply the expected
// version and check rows_affected to detect a lost CAS race. New deployments get
// the column from 0001_initial.sql; this migration is a no-op for them.
type AddRunsVersion struct{}

func (m *AddRunsVersion) Version() int { return 15 }
func (m *AddRunsVersion) Name() string { return "add_runs_version" }

func (m *AddRunsVersion) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(runs)")
	if err != nil {
		return false, fmt.Errorf("pragma table_info runs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "version" {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (m *AddRunsVersion) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN version INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("alter table runs add version: %w", err)
	}
	slog.Info("migrated runs table to add version column")
	return nil
}

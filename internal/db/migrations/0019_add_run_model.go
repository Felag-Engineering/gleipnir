package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddRunModel adds the model column to the runs table on existing deployments.
// New deployments get it from 0001_initial.sql; this migration is a no-op for
// them. SQLite supports ALTER TABLE ADD COLUMN for simple NOT NULL DEFAULT
// columns without needing table recreation.
type AddRunModel struct{}

func (m *AddRunModel) Version() int { return 8 }
func (m *AddRunModel) Name() string { return "add_run_model" }

func (m *AddRunModel) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
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
		if name == "model" {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (m *AddRunModel) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN model TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("alter table runs add model: %w", err)
	}

	slog.Info("migrated runs table to add model column")
	return nil
}

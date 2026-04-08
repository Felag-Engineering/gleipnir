package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddTriggerQueue creates the trigger_queue table and its index on existing
// deployments that were initialized before this table was added. New deployments
// get it from 0001_initial.sql; this migration is a no-op for them.
type AddTriggerQueue struct{}

func (m *AddTriggerQueue) Version() int { return 3 }
func (m *AddTriggerQueue) Name() string { return "add_trigger_queue" }

func (m *AddTriggerQueue) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='trigger_queue'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check trigger_queue existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddTriggerQueue) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE trigger_queue (
    id              TEXT    PRIMARY KEY,
    policy_id       TEXT    NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled')),
    trigger_payload TEXT    NOT NULL,
    position        INTEGER NOT NULL,
    created_at      TEXT    NOT NULL,
    UNIQUE(policy_id, position)
);
CREATE INDEX idx_trigger_queue_policy_position ON trigger_queue(policy_id, position);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create trigger_queue: %w", err)
	}

	slog.Info("migrated: created trigger_queue table")
	return nil
}

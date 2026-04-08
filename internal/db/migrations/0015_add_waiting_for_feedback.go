package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AddWaitingForFeedback updates the runs table CHECK constraint to include
// 'waiting_for_feedback' on existing deployments. New deployments get it from
// 0001_initial.sql directly.
//
// SQLite does not support ALTER COLUMN, so we use the table-recreation pattern.
// The PRAGMA foreign_keys change must be executed OUTSIDE the transaction because
// SQLite does not permit changing it inside a transaction.
type AddWaitingForFeedback struct{}

func (m *AddWaitingForFeedback) Version() int { return 4 }
func (m *AddWaitingForFeedback) Name() string { return "add_waiting_for_feedback" }

func (m *AddWaitingForFeedback) RequiresForeignKeysOff() bool { return true }

func (m *AddWaitingForFeedback) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var tableSQL string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='runs'`,
	).Scan(&tableSQL)
	if err != nil {
		return false, fmt.Errorf("query runs schema: %w", err)
	}
	return strings.Contains(tableSQL, "'waiting_for_feedback'"), nil
}

func (m *AddWaitingForFeedback) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE runs_new (
    id              TEXT    PRIMARY KEY,
    policy_id       TEXT    NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    status          TEXT    NOT NULL CHECK(status IN (
                        'pending',
                        'running',
                        'waiting_for_approval',
                        'waiting_for_feedback',
                        'complete',
                        'failed',
                        'interrupted'
                    )),
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled')),
    trigger_payload TEXT    NOT NULL,
    started_at      TEXT    NOT NULL,
    completed_at    TEXT,
    token_cost      INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    thread_id       TEXT,
    created_at      TEXT    NOT NULL,
    system_prompt   TEXT
);
INSERT INTO runs_new SELECT * FROM runs;
DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;
CREATE INDEX idx_runs_status         ON runs(status);
CREATE INDEX idx_runs_created_at     ON runs(created_at DESC);
CREATE INDEX idx_runs_policy_created ON runs(policy_id, created_at DESC);
CREATE INDEX idx_runs_policy_status  ON runs(policy_id, status);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("recreate runs table with waiting_for_feedback: %w", err)
	}

	slog.Info("migrated runs table to include waiting_for_feedback status")
	return nil
}

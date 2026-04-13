package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AddThinkingStepType updates the run_steps CHECK constraint to include
// 'thinking' on existing deployments where 0001_initial.sql was applied before
// this value existed. New deployments get it directly from 0001_initial.sql.
//
// SQLite does not support ALTER COLUMN to modify CHECK constraints, so we use
// the table-recreation pattern: create a new table, copy data, drop old, rename.
type AddThinkingStepType struct{}

func (m *AddThinkingStepType) Version() int { return 2 }
func (m *AddThinkingStepType) Name() string { return "add_thinking_step_type" }

func (m *AddThinkingStepType) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var tableSQL string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='run_steps'`,
	).Scan(&tableSQL)
	if err != nil {
		return false, fmt.Errorf("query run_steps schema: %w", err)
	}
	return strings.Contains(tableSQL, "'thinking'"), nil
}

func (m *AddThinkingStepType) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE run_steps_new (
    id          TEXT    PRIMARY KEY,
    run_id      TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_number INTEGER NOT NULL,
    type        TEXT    NOT NULL CHECK(type IN (
                    'capability_snapshot',
                    'thought',
                    'thinking',
                    'tool_call',
                    'tool_result',
                    'approval_request',
                    'feedback_request',
                    'feedback_response',
                    'error',
                    'complete'
                )),
    content     TEXT    NOT NULL,
    token_cost  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL,
    UNIQUE(run_id, step_number)
);
INSERT INTO run_steps_new SELECT * FROM run_steps;
DROP TABLE run_steps;
ALTER TABLE run_steps_new RENAME TO run_steps;
CREATE INDEX idx_run_steps_run_step ON run_steps(run_id, step_number);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("recreate run_steps with thinking: %w", err)
	}

	slog.Info("migrated run_steps table to include thinking step type")
	return nil
}

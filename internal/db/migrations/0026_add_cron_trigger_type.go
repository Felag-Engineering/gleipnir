package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AddCronTriggerType updates the CHECK constraints on policies, runs, and
// trigger_queue to include 'cron'. New deployments get this from 0001_initial.sql
// (which already has the updated constraints); this migration is a no-op for them.
//
// SQLite cannot ALTER CHECK constraints, so we use the table-recreation pattern.
// PRAGMA foreign_keys must be toggled OUTSIDE the transaction (SQLite requirement).
type AddCronTriggerType struct{}

func (m *AddCronTriggerType) Version() int { return 16 }
func (m *AddCronTriggerType) Name() string { return "add_cron_trigger_type" }

func (m *AddCronTriggerType) RequiresForeignKeysOff() bool { return true }

func (m *AddCronTriggerType) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var policiesSQL string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='policies'`,
	).Scan(&policiesSQL)
	if err != nil {
		return false, fmt.Errorf("query policies schema: %w", err)
	}
	return strings.Contains(policiesSQL, "'cron'"), nil
}

func (m *AddCronTriggerType) Up(ctx context.Context, tx *sql.Tx) error {
	// Column order for policies_new must exactly match the column order in the
	// current policies table (as seen by existing deployments after migration
	// 0024 added webhook_secret_encrypted via ALTER TABLE ... ADD COLUMN, which
	// appends the column at the end). The INSERT INTO ... SELECT * relies on
	// positional column alignment.
	ddl := `
CREATE TABLE policies_new (
    id                        TEXT PRIMARY KEY,
    name                      TEXT NOT NULL UNIQUE,
    trigger_type              TEXT NOT NULL CHECK(trigger_type IN ('webhook','manual','scheduled','poll','cron')),
    yaml                      TEXT NOT NULL,
    created_at                TEXT NOT NULL,
    updated_at                TEXT NOT NULL,
    paused_at                 TEXT,
    webhook_secret_encrypted  TEXT
);
INSERT INTO policies_new SELECT * FROM policies;
DROP TABLE policies;
ALTER TABLE policies_new RENAME TO policies;
CREATE INDEX idx_policies_trigger_type ON policies(trigger_type);

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
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll', 'cron')),
    trigger_payload TEXT    NOT NULL,
    started_at      TEXT    NOT NULL,
    completed_at    TEXT,
    token_cost      INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    thread_id       TEXT,
    created_at      TEXT    NOT NULL,
    system_prompt   TEXT,
    model           TEXT    NOT NULL DEFAULT '',
    version         INTEGER NOT NULL DEFAULT 0
);
INSERT INTO runs_new SELECT * FROM runs;
DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;
CREATE INDEX idx_runs_status         ON runs(status);
CREATE INDEX idx_runs_created_at     ON runs(created_at DESC);
CREATE INDEX idx_runs_policy_created ON runs(policy_id, created_at DESC);
CREATE INDEX idx_runs_policy_status  ON runs(policy_id, status);

CREATE TABLE trigger_queue_new (
    id              TEXT    PRIMARY KEY,
    policy_id       TEXT    NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll', 'cron')),
    trigger_payload TEXT    NOT NULL,
    position        INTEGER NOT NULL,
    created_at      TEXT    NOT NULL,
    UNIQUE(policy_id, position)
);
INSERT INTO trigger_queue_new SELECT * FROM trigger_queue;
DROP TABLE trigger_queue;
ALTER TABLE trigger_queue_new RENAME TO trigger_queue;
CREATE INDEX idx_trigger_queue_policy_position ON trigger_queue(policy_id, position);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("recreate tables for cron trigger type: %w", err)
	}

	slog.Info("migrated tables to include cron trigger type")
	return nil
}

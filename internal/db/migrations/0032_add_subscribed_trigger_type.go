package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AddSubscribedTriggerType updates the CHECK constraints on policies, runs, and
// trigger_queue to include 'subscribed'. New deployments get this from
// 0001_initial.sql (which already has the updated constraints); this migration
// is a no-op for them.
//
// SQLite cannot ALTER CHECK constraints, so we use the table-recreation pattern.
// PRAGMA foreign_keys must be toggled OUTSIDE the transaction (SQLite requirement).
type AddSubscribedTriggerType struct{}

func (m *AddSubscribedTriggerType) Version() int { return 22 }
func (m *AddSubscribedTriggerType) Name() string { return "add_subscribed_trigger_type" }

func (m *AddSubscribedTriggerType) RequiresForeignKeysOff() bool { return true }

func (m *AddSubscribedTriggerType) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var policiesSQL string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='policies'`,
	).Scan(&policiesSQL)
	if err != nil {
		return false, fmt.Errorf("query policies schema: %w", err)
	}
	return strings.Contains(policiesSQL, "'subscribed'"), nil
}

func (m *AddSubscribedTriggerType) Up(ctx context.Context, tx *sql.Tx) error {
	// Column order for *_new tables must exactly match the column order in the
	// current tables (as seen by existing deployments after all prior migrations).
	// The INSERT INTO ... SELECT * relies on positional column alignment.
	ddl := `
CREATE TABLE policies_new (
    id                        TEXT PRIMARY KEY,
    name                      TEXT NOT NULL UNIQUE,
    trigger_type              TEXT NOT NULL CHECK(trigger_type IN ('webhook','manual','scheduled','poll','cron','subscribed')),
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
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll', 'cron', 'subscribed')),
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
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll', 'cron', 'subscribed')),
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
		return fmt.Errorf("recreate tables for subscribed trigger type: %w", err)
	}

	slog.Info("migrated tables to include subscribed trigger type")
	return nil
}

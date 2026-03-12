-- Migration: 0004
-- Adds 'scheduled' to the trigger_type CHECK constraints on policies and runs,
-- and adds the paused_at column to policies for auto-pause after all fire times fire.
--
-- SQLite does not support ALTER TABLE ... ALTER COLUMN, so we rebuild both
-- tables using the standard SQLite table-rebuild pattern:
--   1. Create the new table with the updated CHECK constraint.
--   2. Copy all rows from the old table.
--   3. Drop the old table.
--   4. Rename the new table.
--   5. Recreate indexes.

PRAGMA foreign_keys = OFF;

BEGIN;

-- ---------------------------------------------------------------------------
-- Rebuild policies
-- ---------------------------------------------------------------------------

CREATE TABLE policies_new (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL UNIQUE,
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'cron', 'poll', 'manual', 'scheduled')),
    yaml            TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    paused_at       TEXT                  -- nullable, ISO 8601 UTC; set when a scheduled policy exhausts all fire times
);

INSERT INTO policies_new SELECT id, name, trigger_type, yaml, created_at, updated_at, NULL FROM policies;
DROP TABLE policies;
ALTER TABLE policies_new RENAME TO policies;

CREATE INDEX idx_policies_trigger_type ON policies(trigger_type);

-- ---------------------------------------------------------------------------
-- Rebuild runs
-- ---------------------------------------------------------------------------

CREATE TABLE runs_new (
    id              TEXT    PRIMARY KEY,
    policy_id       TEXT    NOT NULL REFERENCES policies(id),
    status          TEXT    NOT NULL CHECK(status IN (
                        'pending',
                        'running',
                        'waiting_for_approval',
                        'complete',
                        'failed',
                        'interrupted'
                    )),
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'cron', 'poll', 'manual', 'scheduled')),
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

CREATE INDEX idx_runs_policy_id ON runs(policy_id);
CREATE INDEX idx_runs_status    ON runs(status);

-- ---------------------------------------------------------------------------
-- Record migration
-- ---------------------------------------------------------------------------

INSERT INTO schema_migrations(version, applied_at)
VALUES (4, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));

COMMIT;

PRAGMA foreign_keys = ON;

-- Migration: 0009
-- Adds composite index on runs(policy_id, status) for fast active-run detection
-- in CheckConcurrency and ListRuns queries that filter by both columns.

BEGIN;

CREATE INDEX IF NOT EXISTS idx_runs_policy_status ON runs(policy_id, status);

INSERT INTO schema_migrations(version, applied_at)
VALUES (9, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));

COMMIT;

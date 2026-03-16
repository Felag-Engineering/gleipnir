-- Migration: 0005
-- Adds composite and single-column indexes for high-frequency queries on runs,
-- run_steps, and approval_requests to avoid full table scans as data grows.
--
-- idx_run_steps_run_id is replaced by idx_run_steps_run_step (composite) since
-- all step fetches are ordered by step_number and the single-column index
-- provides no benefit over the UNIQUE(run_id, step_number) constraint alone.

BEGIN;

-- ---------------------------------------------------------------------------
-- Runs: cover list-by-policy and global list-by-date queries
-- ---------------------------------------------------------------------------

CREATE INDEX IF NOT EXISTS idx_runs_created_at       ON runs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_policy_created   ON runs(policy_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Approval requests: cover the timeout scanner and per-run pending lookup
-- ---------------------------------------------------------------------------

CREATE INDEX IF NOT EXISTS idx_approval_requests_status_expires ON approval_requests(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_approval_requests_run_pending    ON approval_requests(run_id, status);

-- ---------------------------------------------------------------------------
-- Run steps: replace single-column run_id index with composite run_id+step
-- ---------------------------------------------------------------------------

DROP INDEX IF EXISTS idx_run_steps_run_id;
CREATE INDEX IF NOT EXISTS idx_run_steps_run_step ON run_steps(run_id, step_number);

-- ---------------------------------------------------------------------------
-- Record migration
-- ---------------------------------------------------------------------------

INSERT INTO schema_migrations(version, applied_at)
VALUES (5, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));

COMMIT;

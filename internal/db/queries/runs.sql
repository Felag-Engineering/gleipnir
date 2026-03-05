-- name: CreateRun :one
INSERT INTO runs (id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
VALUES (?, ?, 'pending', ?, ?, ?, ?)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE id = ?;

-- name: ListRunsByPolicy :many
SELECT * FROM runs WHERE policy_id = ? ORDER BY created_at DESC;

-- name: UpdateRunStatus :exec
UPDATE runs SET status = ?, completed_at = ? WHERE id = ?;

-- name: UpdateRunError :exec
UPDATE runs SET status = ?, error = ?, completed_at = ? WHERE id = ?;

-- name: IncrementRunTokenCost :exec
UPDATE runs SET token_cost = token_cost + ? WHERE id = ?;

-- name: MarkInterruptedRuns :exec
UPDATE runs
SET status = 'interrupted', completed_at = ?
WHERE status IN ('running', 'waiting_for_approval');

-- CreateRun: note that started_at is recorded at pending-creation time, not at
-- the running transition. Accurate run-start timing will require making
-- started_at nullable and a schema migration -- tracked as a separate issue.
-- name: CreateRun :one
INSERT INTO runs (id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
VALUES (:id, :policy_id, 'pending', :trigger_type, :trigger_payload, :started_at, :created_at)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE id = :id;

-- name: ListRunsByPolicy :many
SELECT * FROM runs WHERE policy_id = :policy_id ORDER BY created_at DESC;

-- name: UpdateRunStatus :exec
UPDATE runs SET status = :status, completed_at = :completed_at WHERE id = :id;

-- name: UpdateRunError :exec
UPDATE runs SET status = :status, error = :error, completed_at = :completed_at WHERE id = :id;

-- name: IncrementRunTokenCost :exec
UPDATE runs SET token_cost = token_cost + :token_cost WHERE id = :id;

-- name: ListOrphanedRuns :many
SELECT * FROM runs WHERE status IN ('running', 'waiting_for_approval');

-- name: ListRunsByStatus :many
SELECT * FROM runs WHERE status = :status ORDER BY created_at ASC;

-- UpdateRunThreadID: populated once when the first approval notification creates
-- a Slack thread (see EPIC-010). Must only be called on a run without a thread_id.
-- name: UpdateRunThreadID :exec
UPDATE runs SET thread_id = :thread_id WHERE id = :id;

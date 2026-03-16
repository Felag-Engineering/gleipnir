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

-- name: ListActiveRunsByPolicy :many
SELECT * FROM runs
WHERE policy_id = :policy_id
  AND status IN ('pending', 'running', 'waiting_for_approval')
ORDER BY created_at ASC;

-- name: ListRuns :many
SELECT * FROM runs
WHERE (sqlc.narg('policy_id') IS NULL OR policy_id = sqlc.narg('policy_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateRunSystemPrompt :exec
UPDATE runs SET system_prompt = :system_prompt WHERE id = :id;

-- name: CountActiveRuns :one
SELECT COUNT(*) FROM runs WHERE status IN ('pending', 'running', 'waiting_for_approval');

-- name: SumTokensLast24Hours :one
SELECT COALESCE(SUM(token_cost), 0) FROM runs WHERE created_at >= :since;

-- name: DeleteRunsByPolicy :exec
DELETE FROM runs WHERE policy_id = :policy_id;

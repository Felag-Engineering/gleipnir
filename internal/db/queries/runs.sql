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
  AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until') IS NULL OR created_at <= sqlc.narg('until'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListRunsAsc :many
SELECT * FROM runs
WHERE (sqlc.narg('policy_id') IS NULL OR policy_id = sqlc.narg('policy_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until') IS NULL OR created_at <= sqlc.narg('until'))
ORDER BY created_at ASC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListRunsByTokenCostDesc :many
SELECT * FROM runs
WHERE (sqlc.narg('policy_id') IS NULL OR policy_id = sqlc.narg('policy_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until') IS NULL OR created_at <= sqlc.narg('until'))
ORDER BY token_cost DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListRunsByTokenCostAsc :many
SELECT * FROM runs
WHERE (sqlc.narg('policy_id') IS NULL OR policy_id = sqlc.narg('policy_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until') IS NULL OR created_at <= sqlc.narg('until'))
ORDER BY token_cost ASC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListRunsByDurationDesc :many
SELECT * FROM runs
WHERE (sqlc.narg('policy_id') IS NULL OR policy_id = sqlc.narg('policy_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until') IS NULL OR created_at <= sqlc.narg('until'))
ORDER BY CASE WHEN completed_at IS NULL THEN 1 ELSE 0 END ASC, (julianday(completed_at) - julianday(started_at)) DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListRunsByDurationAsc :many
SELECT * FROM runs
WHERE (sqlc.narg('policy_id') IS NULL OR policy_id = sqlc.narg('policy_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until') IS NULL OR created_at <= sqlc.narg('until'))
ORDER BY CASE WHEN completed_at IS NULL THEN 1 ELSE 0 END ASC, (julianday(completed_at) - julianday(started_at)) ASC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountRuns :one
SELECT COUNT(*) FROM runs
WHERE (sqlc.narg('policy_id') IS NULL OR policy_id = sqlc.narg('policy_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until') IS NULL OR created_at <= sqlc.narg('until'));

-- name: UpdateRunSystemPrompt :exec
UPDATE runs SET system_prompt = :system_prompt WHERE id = :id;

-- name: CountActiveRuns :one
SELECT COUNT(*) FROM runs WHERE status IN ('pending', 'running', 'waiting_for_approval');

-- name: SumTokensLast24Hours :one
SELECT CAST(COALESCE(SUM(token_cost), 0) AS INTEGER) FROM runs WHERE created_at >= :since;

-- name: HasScheduledRunSince :one
SELECT EXISTS(SELECT 1 FROM runs WHERE policy_id = :policy_id AND trigger_type = 'scheduled' AND created_at >= :since) AS fired;

-- name: DeleteRunsByPolicy :exec
DELETE FROM runs WHERE policy_id = :policy_id;

-- name: ListRunsWithPolicyName :many
SELECT r.*, COALESCE(p.name, '') AS policy_name
FROM runs r
LEFT JOIN policies p ON r.policy_id = p.id
WHERE (sqlc.narg('policy_id') IS NULL OR r.policy_id = sqlc.narg('policy_id'))
  AND (sqlc.narg('status') IS NULL OR r.status = sqlc.narg('status'))
ORDER BY r.created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

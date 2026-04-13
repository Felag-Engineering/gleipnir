-- CreateRun: note that started_at is recorded at pending-creation time, not at
-- the running transition. Accurate run-start timing will require making
-- started_at nullable and a schema migration -- tracked as a separate issue.
-- name: CreateRun :one
INSERT INTO runs (id, policy_id, model, status, trigger_type, trigger_payload, started_at, created_at)
VALUES (:id, :policy_id, :model, 'pending', :trigger_type, :trigger_payload, :started_at, :created_at)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE id = :id;

-- name: UpdateRunStatus :exec
UPDATE runs SET status = :status, completed_at = :completed_at WHERE id = :id;

-- name: UpdateRunError :exec
UPDATE runs SET status = :status, error = :error, completed_at = :completed_at WHERE id = :id;

-- name: IncrementRunTokenCost :exec
UPDATE runs SET token_cost = token_cost + :token_cost WHERE id = :id;

-- Source of truth for terminal statuses: model.IsTerminalStatus (internal/model/model.go).
-- Pending is excluded because pending->interrupted is not a legal state transition.
-- name: ListOrphanedRuns :many
SELECT * FROM runs WHERE status IN ('running', 'waiting_for_approval', 'waiting_for_feedback');

-- Active = !model.IsTerminalStatus (internal/model/model.go). Keep this IN list in sync.
-- name: ListActiveRunsByPolicy :many
SELECT * FROM runs
WHERE policy_id = :policy_id
  AND status IN ('pending', 'running', 'waiting_for_approval', 'waiting_for_feedback')
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

-- Active = !model.IsTerminalStatus (internal/model/model.go). Keep this IN list in sync.
-- name: CountActiveRuns :one
SELECT COUNT(*) FROM runs WHERE status IN ('pending', 'running', 'waiting_for_approval', 'waiting_for_feedback');

-- name: SumTokensLast24Hours :one
SELECT CAST(COALESCE(SUM(token_cost), 0) AS INTEGER) FROM runs WHERE created_at >= :since;

-- name: HasScheduledRunSince :one
SELECT EXISTS(SELECT 1 FROM runs WHERE policy_id = :policy_id AND trigger_type = 'scheduled' AND created_at >= :since) AS fired;

-- GetRunTimeSeries returns hourly-bucketed run counts and token costs grouped by
-- status and model for the dashboard time-series charts. The bucket column is
-- truncated to the hour boundary using strftime so the Go handler can build a
-- consistent x-axis without holes.
-- name: GetRunTimeSeries :many
SELECT
  strftime('%Y-%m-%dT%H:00:00Z', created_at) AS bucket,
  status,
  model,
  COUNT(*) AS run_count,
  CAST(COALESCE(SUM(token_cost), 0) AS INTEGER) AS total_tokens
FROM runs
WHERE created_at >= :since
GROUP BY bucket, status, model
ORDER BY bucket ASC;

-- ListAttentionItems returns pending approval requests and pending feedback
-- requests joined with their parent runs and policies for the attention queue.
-- expires_at is COALESCE'd to '' so the UNION columns are uniformly non-null;
-- the Go handler converts '' back to nil before returning to the client.
-- name: ListAttentionItems :many
SELECT
  'approval' AS item_type,
  ar.id AS request_id,
  r.id AS run_id,
  r.policy_id,
  p.name AS policy_name,
  ar.tool_name,
  '' AS message,
  ar.expires_at,
  ar.created_at
FROM approval_requests ar
JOIN runs r ON ar.run_id = r.id
JOIN policies p ON r.policy_id = p.id
WHERE ar.status = 'pending' AND r.status = 'waiting_for_approval'
UNION ALL
SELECT
  'feedback' AS item_type,
  fr.id AS request_id,
  r.id AS run_id,
  r.policy_id,
  p.name AS policy_name,
  fr.tool_name,
  fr.message,
  COALESCE(fr.expires_at, ''),
  fr.created_at
FROM feedback_requests fr
JOIN runs r ON fr.run_id = r.id
JOIN policies p ON r.policy_id = p.id
WHERE fr.status = 'pending' AND r.status = 'waiting_for_feedback'
ORDER BY 9 ASC;


-- name: CreateRunStep :one
INSERT INTO run_steps (id, run_id, step_number, type, content, token_cost, created_at)
VALUES (:id, :run_id, :step_number, :type, :content, :token_cost, :created_at)
RETURNING *;

-- name: CreateRunStepNextNumber :one
-- Inserts a run step deriving step_number atomically as MAX(step_number)+1 for
-- the run (0 for the first step). Used by writers that do NOT share the agent
-- AuditWriter's in-memory counter (e.g. the timeout scanner) so out-of-band
-- inserts can never reuse or collide with an existing number. See issue #484.
INSERT INTO run_steps (id, run_id, step_number, type, content, token_cost, created_at)
VALUES (
  :id,
  :run_id,
  COALESCE((SELECT MAX(step_number) + 1 FROM run_steps WHERE run_id = :run_id), 0),
  :type,
  :content,
  :token_cost,
  :created_at
)
RETURNING *;

-- name: ListRunSteps :many
SELECT * FROM run_steps
WHERE run_id = :run_id
  AND step_number > :after
ORDER BY step_number ASC
LIMIT :limit;

-- name: CountRunSteps :one
SELECT COUNT(*) FROM run_steps WHERE run_id = :run_id;

-- name: GetLatestRunStep :one
SELECT * FROM run_steps WHERE run_id = :run_id ORDER BY step_number DESC LIMIT 1;


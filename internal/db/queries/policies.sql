-- name: CreatePolicy :one
INSERT INTO policies (id, name, trigger_type, yaml, created_at, updated_at)
VALUES (:id, :name, :trigger_type, :yaml, :created_at, :updated_at)
RETURNING *;

-- name: GetPolicy :one
SELECT * FROM policies WHERE id = :id;

-- name: GetPolicyByName :one
SELECT * FROM policies WHERE name = :name;

-- name: ListPolicies :many
SELECT * FROM policies ORDER BY created_at DESC;

-- name: UpdatePolicy :one
UPDATE policies
SET name = :name, trigger_type = :trigger_type, yaml = :yaml, updated_at = :updated_at
WHERE id = :id
RETURNING *;

-- name: DeletePolicy :exec
DELETE FROM policies WHERE id = :id;

-- name: GetScheduledActivePolicies :many
SELECT * FROM policies WHERE trigger_type = 'scheduled' AND paused_at IS NULL;

-- name: SetPolicyPausedAt :exec
UPDATE policies SET paused_at = :paused_at WHERE id = :id;

-- name: ClearPolicyPausedAt :exec
UPDATE policies SET paused_at = NULL WHERE id = :id;

-- name: CountPolicies :one
SELECT COUNT(*) FROM policies;

-- name: GetPollActivePolicies :many
SELECT * FROM policies WHERE trigger_type = 'poll' AND paused_at IS NULL;

-- name: ListPoliciesWithLatestRun :many
SELECT
    p.id,
    p.name,
    p.trigger_type,
    p.yaml,
    p.created_at,
    p.updated_at,
    p.paused_at,
    r.id          AS run_id,
    r.status      AS run_status,
    r.started_at  AS run_started_at,
    r.token_cost  AS run_token_cost,
    (SELECT CAST(COALESCE(AVG(token_cost), 0) AS INTEGER)
     FROM runs WHERE policy_id = p.id) AS avg_token_cost
FROM policies p
LEFT JOIN runs r ON r.id = (
    SELECT id FROM runs
    WHERE policy_id = p.id
    ORDER BY created_at DESC
    LIMIT 1
)
ORDER BY p.created_at DESC;

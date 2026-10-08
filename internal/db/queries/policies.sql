-- name: CreatePolicy :one
INSERT INTO policies (id, name, trigger_type, yaml, created_at, updated_at)
VALUES (:id, :name, :trigger_type, :yaml, :created_at, :updated_at)
RETURNING *;

-- Deleting an agent archives it (deleted_at) so its run history survives
-- (#1052). Every policy query below filters archived rows out by default, so a
-- path that forgets to think about archival fails closed (not found). Only
-- GetPolicyIncludingArchived - for read-only attribution of historical runs - 
-- sees them.
-- name: GetPolicy :one
SELECT * FROM policies WHERE id = :id AND deleted_at IS NULL;

-- name: GetPolicyIncludingArchived :one
SELECT * FROM policies WHERE id = :id;

-- name: GetPolicyByName :one
SELECT * FROM policies WHERE name = :name AND deleted_at IS NULL;

-- name: ListPolicies :many
SELECT * FROM policies WHERE deleted_at IS NULL ORDER BY created_at DESC;

-- name: UpdatePolicy :one
-- Note: webhook_secret_encrypted is intentionally excluded from this UPDATE so
-- that policy edits (name, yaml, trigger_type) do not clear the stored secret.
-- To manage the secret, use SetPolicyWebhookSecret / ClearPolicyWebhookSecret.
UPDATE policies
SET name = :name, trigger_type = :trigger_type, yaml = :yaml, updated_at = :updated_at
WHERE id = :id AND deleted_at IS NULL
RETURNING *;

-- name: ArchivePolicy :execrows
-- The webhook secret is cleared so the archived agent's webhook can never
-- authenticate again, and so key rotation has nothing to carry for it.
UPDATE policies
SET deleted_at = :deleted_at, updated_at = :updated_at, webhook_secret_encrypted = NULL
WHERE id = :id AND deleted_at IS NULL;

-- name: GetScheduledActivePolicies :many
SELECT * FROM policies WHERE trigger_type = 'scheduled' AND paused_at IS NULL AND deleted_at IS NULL;

-- name: SetPolicyPausedAt :exec
UPDATE policies SET paused_at = :paused_at WHERE id = :id;

-- name: ClearPolicyPausedAt :exec
UPDATE policies SET paused_at = NULL WHERE id = :id;

-- name: CountPolicies :one
SELECT COUNT(*) FROM policies WHERE deleted_at IS NULL;

-- name: GetPollActivePolicies :many
SELECT * FROM policies WHERE trigger_type = 'poll' AND paused_at IS NULL AND deleted_at IS NULL;

-- name: GetCronActivePolicies :many
SELECT * FROM policies WHERE trigger_type = 'cron' AND paused_at IS NULL AND deleted_at IS NULL;

-- name: GetSubscribedActivePolicies :many
SELECT * FROM policies WHERE trigger_type = 'subscribed' AND paused_at IS NULL AND deleted_at IS NULL;

-- name: SetPolicyWebhookSecret :exec
UPDATE policies SET webhook_secret_encrypted = :ciphertext, updated_at = :updated_at WHERE id = :id AND deleted_at IS NULL;

-- name: GetPolicyWebhookSecret :one
SELECT webhook_secret_encrypted FROM policies WHERE id = :id AND deleted_at IS NULL;

-- name: ClearPolicyWebhookSecret :exec
UPDATE policies SET webhook_secret_encrypted = NULL, updated_at = :updated_at WHERE id = :id;

-- name: ListPolicyWebhookSecrets :many
SELECT id, webhook_secret_encrypted
FROM policies
WHERE webhook_secret_encrypted IS NOT NULL
ORDER BY id;

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
     FROM runs WHERE policy_id = p.id) AS avg_token_cost,
    (SELECT COUNT(*) FROM runs WHERE policy_id = p.id) AS run_count
FROM policies p
LEFT JOIN runs r ON r.id = (
    SELECT id FROM runs
    WHERE policy_id = p.id
    ORDER BY created_at DESC
    LIMIT 1
)
WHERE p.deleted_at IS NULL
ORDER BY p.created_at DESC;

-- name: CreatePluginContainer :one
INSERT INTO plugin_containers (
    id, plugin_instance_id, image_ref, image_digest, config_hash, network_name,
    memory_limit_bytes, cpu_limit_millicores, desired_state, created_at, updated_at
)
VALUES (
    :id, :plugin_instance_id, :image_ref, :image_digest, :config_hash, :network_name,
    :memory_limit_bytes, :cpu_limit_millicores, :desired_state, :created_at, :updated_at
)
RETURNING *;

-- name: GetPluginContainer :one
SELECT * FROM plugin_containers WHERE id = :id;

-- name: GetPluginContainerByInstance :one
SELECT * FROM plugin_containers WHERE plugin_instance_id = :plugin_instance_id;

-- ListPluginContainers returns every desired-state row. This is the left side
-- of the reconciler's diff: the whole desired set, compared against whatever a
-- label query returns from the runtime. It is deliberately unfiltered and
-- unpaginated -- a homelab has tens of instances, and a partial desired set
-- would make the diff wrong rather than merely slow.
-- name: ListPluginContainers :many
SELECT * FROM plugin_containers ORDER BY plugin_instance_id;

-- UpdatePluginContainerDesiredState rewrites the desired state for one
-- instance under an ADR-038 CAS guard: rows_affected == 0 means another writer
-- moved the row first and this caller's read was stale. Callers must not
-- assume their write won.
-- name: UpdatePluginContainerDesiredState :execrows
UPDATE plugin_containers
SET image_ref = :image_ref,
    image_digest = :image_digest,
    config_hash = :config_hash,
    network_name = :network_name,
    memory_limit_bytes = :memory_limit_bytes,
    cpu_limit_millicores = :cpu_limit_millicores,
    desired_state = :desired_state,
    version = version + 1,
    updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- name: DeletePluginContainer :exec
DELETE FROM plugin_containers WHERE id = :id;

-- name: CreateContainerGeneration :one
INSERT INTO plugin_container_generations (
    id, plugin_instance_id, generation, container_id, image_digest, config_hash,
    token_hash, status, created_at, updated_at
)
VALUES (
    :id, :plugin_instance_id, :generation, :container_id, :image_digest, :config_hash,
    :token_hash, :status, :created_at, :updated_at
)
RETURNING *;

-- name: GetContainerGeneration :one
SELECT * FROM plugin_container_generations WHERE id = :id;

-- GetContainerGenerationByTokenHash is the authentication lookup: a plugin
-- presents its instance token, the host hashes it and finds the generation it
-- belongs to. Revoked tokens are excluded here rather than by the caller, so
-- there is no path that authenticates a revoked generation by forgetting a
-- check.
-- name: GetContainerGenerationByTokenHash :one
SELECT * FROM plugin_container_generations
WHERE token_hash = :token_hash AND token_revoked_at IS NULL;

-- name: ListContainerGenerationsByInstance :many
SELECT * FROM plugin_container_generations
WHERE plugin_instance_id = :plugin_instance_id
ORDER BY generation DESC;

-- ListLiveContainerGenerations returns every generation that still owns a
-- container the reconciler may have to act on -- the right side of its diff.
-- Terminal generations (stopped, failed) are excluded: their containers are
-- gone, so nothing about them can converge.
-- name: ListLiveContainerGenerations :many
SELECT * FROM plugin_container_generations
WHERE status IN ('pending', 'starting', 'healthy', 'active', 'draining')
ORDER BY plugin_instance_id, generation;

-- GetLatestContainerGeneration returns the highest-numbered generation for an
-- instance, whatever its status. Rotation reads it to mint the next number, so
-- it must NOT skip terminal rows: a generation number is never reused, even
-- after its container is gone.
-- name: GetLatestContainerGeneration :one
SELECT * FROM plugin_container_generations
WHERE plugin_instance_id = :plugin_instance_id
ORDER BY generation DESC
LIMIT 1;

-- UpdateContainerGenerationStatus moves a generation through the rotation
-- lifecycle. The expected-status guard makes each move a CAS: rows_affected ==
-- 0 means the generation was not where the caller thought it was, which during
-- a rotation means another pass already advanced it.
-- name: UpdateContainerGenerationStatus :execrows
UPDATE plugin_container_generations
SET status = :status,
    status_detail = :status_detail,
    updated_at = :updated_at
WHERE id = :id AND status = :expected_status;

-- SetContainerGenerationContainerID records the runtime-assigned container id
-- once the container exists. Guarded on container_id IS NULL so a second
-- create attempt cannot silently orphan the first container by overwriting the
-- only reference to it.
-- name: SetContainerGenerationContainerID :execrows
UPDATE plugin_container_generations
SET container_id = :container_id,
    updated_at = :updated_at
WHERE id = :id AND container_id IS NULL;

-- RevokeContainerGenerationToken invalidates a generation's instance token.
-- Guarded on token_revoked_at IS NULL so revocation is idempotent from the
-- caller's perspective and the original revocation time is never overwritten
-- by a later pass.
-- name: RevokeContainerGenerationToken :execrows
UPDATE plugin_container_generations
SET token_revoked_at = :token_revoked_at,
    updated_at = :updated_at
WHERE id = :id AND token_revoked_at IS NULL;

-- name: DeleteContainerGeneration :exec
DELETE FROM plugin_container_generations WHERE id = :id;

-- ListPurgeableGenerationTokens returns generations whose instance token was
-- revoked longer ago than the retention window and whose hash is still stored.
--
-- The window exists because a revoked token is briefly still useful: a host RPC
-- that arrived just before the revocation is correlated to its generation by
-- hash, and purging on the instant of revocation would make the last few
-- moments of a generation's life unattributable. After that it is only
-- material, so it goes.
--
-- The 'purged:%' guard makes the sweep idempotent. It matches the tombstone
-- the purge writes -- token_hash is NOT NULL UNIQUE, so the hash is replaced
-- by a per-row tombstone rather than cleared, and the row itself survives.
-- Deleting the row instead would be worse in two ways: it would discard the
-- rotation history an operator reads to answer "what has this instance run",
-- and deleting the highest-numbered row would let the next rotation reuse a
-- generation number that a stale container may still be labelled with.
-- name: ListPurgeableGenerationTokens :many
SELECT * FROM plugin_container_generations
WHERE token_revoked_at IS NOT NULL
  AND token_revoked_at < :cutoff
  AND token_hash NOT LIKE 'purged:%'
ORDER BY token_revoked_at
LIMIT :limit;

-- PurgeContainerGenerationToken replaces a revoked generation's token hash with
-- a tombstone. Guarded on the revocation being set and the hash not already
-- being a tombstone, so a live generation's token can never be purged by a
-- racing sweep and a repeated sweep is a no-op.
-- name: PurgeContainerGenerationToken :execrows
UPDATE plugin_container_generations
SET token_hash = :token_hash,
    updated_at = :updated_at
WHERE id = :id
  AND token_revoked_at IS NOT NULL
  AND token_hash NOT LIKE 'purged:%';

-- UpsertContainerImage records a loaded OCI image. Loading the same digest
-- again is not an error -- a reinstall or a second instance of the same plugin
-- reaches the same bytes -- so the conflict path refreshes the mutable fields
-- instead of failing.
-- name: UpsertContainerImage :one
INSERT INTO plugin_container_images (digest, reference, plugin_id, size_bytes, loaded_at, last_used_at)
VALUES (:digest, :reference, :plugin_id, :size_bytes, :loaded_at, :last_used_at)
ON CONFLICT(digest) DO UPDATE SET
    reference = excluded.reference,
    plugin_id = excluded.plugin_id,
    size_bytes = excluded.size_bytes,
    last_used_at = excluded.last_used_at
RETURNING *;

-- name: GetContainerImage :one
SELECT * FROM plugin_container_images WHERE digest = :digest;

-- name: ListContainerImages :many
SELECT * FROM plugin_container_images ORDER BY loaded_at;

-- name: TouchContainerImage :execrows
UPDATE plugin_container_images SET last_used_at = :last_used_at WHERE digest = :digest;

-- CountContainerImageReferences counts the live generations pinned to a
-- digest. The count is derived on every read rather than stored in a column:
-- a hand-maintained counter drifts, and a drifted count here means either
-- deleting an image a generation still runs or never reclaiming one at all.
-- Terminal generations do not count -- their containers are gone.
-- name: CountContainerImageReferences :one
SELECT COUNT(*) FROM plugin_container_generations
WHERE image_digest = :image_digest
  AND status IN ('pending', 'starting', 'healthy', 'active', 'draining');

-- ListUnreferencedContainerImages returns the digests GC may reclaim: images
-- no live generation still runs. Ordered oldest-first so a bounded GC pass
-- reclaims the stalest images before the newest.
-- name: ListUnreferencedContainerImages :many
SELECT * FROM plugin_container_images
WHERE digest NOT IN (
    SELECT image_digest FROM plugin_container_generations
    WHERE status IN ('pending', 'starting', 'healthy', 'active', 'draining')
)
ORDER BY loaded_at;

-- name: DeleteContainerImage :exec
DELETE FROM plugin_container_images WHERE digest = :digest;

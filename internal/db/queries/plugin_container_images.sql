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

-- CountContainerImageReferences counts what pins a digest: live generations
-- plus pending manifests naming it. The count is derived on every read rather
-- than stored in a column: a hand-maintained counter drifts, and a drifted
-- count here means either deleting an image a generation still runs or never
-- reclaiming one at all. Terminal generations do not count -- their
-- containers are gone. A pending manifest counts because accepting it makes
-- its image the desired one, and the host does not reload an image on accept;
-- the digest is matched as a substring of the candidate manifest text, which
-- is where a v2 manifest pins it. A desired-state row counts too: accepting a
-- manifest moves plugin_containers.image_digest and deletes the pending row,
-- and a stopped instance has no generation yet, so without this the image
-- would be unpinned between accept and the next start.
-- name: CountContainerImageReferences :one
SELECT
    (SELECT COUNT(*) FROM plugin_container_generations
      WHERE plugin_container_generations.image_digest = :image_digest
        AND status IN ('pending', 'starting', 'healthy', 'active', 'draining'))
  + (SELECT COUNT(*) FROM plugin_pending_manifests
      WHERE instr(candidate_manifest, CAST(:image_digest AS TEXT)) > 0)
  + (SELECT COUNT(*) FROM plugin_containers
      WHERE plugin_containers.image_digest = :image_digest);

-- ListUnreferencedContainerImages returns the digests GC may reclaim: images
-- no live generation still runs, no desired-state row wants, and no pending
-- manifest names. Ordered oldest-first so a bounded GC pass
-- reclaims the stalest images before the newest.
-- name: ListUnreferencedContainerImages :many
SELECT * FROM plugin_container_images
WHERE digest NOT IN (
    SELECT image_digest FROM plugin_container_generations
    WHERE status IN ('pending', 'starting', 'healthy', 'active', 'draining')
)
AND digest NOT IN (SELECT image_digest FROM plugin_containers)
AND NOT EXISTS (
    SELECT 1 FROM plugin_pending_manifests
    WHERE instr(plugin_pending_manifests.candidate_manifest, plugin_container_images.digest) > 0
)
ORDER BY loaded_at;

-- name: DeleteContainerImage :exec
DELETE FROM plugin_container_images WHERE digest = :digest;

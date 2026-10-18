-- name: SetAdminIdentity :exec
-- Sets the admin-managed mapping of a user to an actor id on one plugin
-- instance, replacing any earlier mapping for that (instance, user) pair. An
-- actor id already mapped to a different user on the same instance fails the
-- UNIQUE(instance_id, external_user_id) constraint; callers report it as 409.
INSERT INTO plugin_user_identities (instance_id, user_id, external_user_id, link_method, verified_at, created_at)
VALUES (:instance_id, :user_id, :external_user_id, 'admin_set', NULL, :created_at)
ON CONFLICT (instance_id, user_id) DO UPDATE SET
    external_user_id = excluded.external_user_id,
    link_method      = 'admin_set',
    verified_at      = NULL;

-- name: DeleteIdentity :execrows
DELETE FROM plugin_user_identities WHERE instance_id = :instance_id AND user_id = :user_id;

-- name: ListIdentitiesByUser :many
SELECT pui.instance_id, pi.instance_name, pui.external_user_id, pui.link_method, pui.verified_at, pui.created_at
FROM plugin_user_identities pui
JOIN plugin_instances pi ON pi.id = pui.instance_id
WHERE pui.user_id = :user_id
ORDER BY pi.instance_name;

-- name: GetUserByExternalIdentity :many
-- Returns one row per role for the active user an admin mapped to the given
-- actor id on the given instance. A deactivated user, or a user with no roles,
-- produces no rows and is treated as unmapped. The instance is part of the
-- key: an actor id mapped on one instance means nothing on another.
SELECT u.id, u.username, ur.role
FROM plugin_user_identities pui
JOIN users u ON u.id = pui.user_id
JOIN user_roles ur ON ur.user_id = u.id
WHERE pui.instance_id = :instance_id
  AND pui.external_user_id = :external_user_id
  AND u.deactivated_at IS NULL
ORDER BY ur.role;

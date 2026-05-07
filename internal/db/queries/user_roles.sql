-- name: AssignRole :exec
INSERT OR IGNORE INTO user_roles (user_id, role, created_at)
VALUES (:user_id, :role, :created_at);

-- name: RemoveRole :exec
DELETE FROM user_roles WHERE user_id = :user_id AND role = :role;

-- name: RemoveAllRolesForUser :exec
DELETE FROM user_roles WHERE user_id = :user_id;

-- name: ListRolesByUser :many
SELECT role FROM user_roles WHERE user_id = :user_id ORDER BY role;

-- name: HasRole :one
SELECT COUNT(*) FROM user_roles WHERE user_id = :user_id AND role = :role;

-- name: ListAllUserRoles :many
SELECT user_id, role FROM user_roles ORDER BY user_id, role;

-- name: ListUsersByRole :many
SELECT ur.user_id, u.username
FROM user_roles ur
JOIN users u ON u.id = ur.user_id
WHERE ur.role = :role
ORDER BY u.username;

-- name: ListActiveUsersByRole :many
SELECT ur.user_id, u.username
FROM user_roles ur
JOIN users u ON u.id = ur.user_id
WHERE ur.role = :role AND u.deactivated_at IS NULL
ORDER BY u.username;

-- ListAllActiveUsersWithRoles returns one row per (user, role) pair for
-- all non-deactivated users, used by the plugin host-service UserDirectoryRead
-- handler. The 1:1 row-to-UserEntry mapping avoids GROUP_CONCAT.
-- name: ListAllActiveUsersWithRoles :many
SELECT u.id AS user_id, u.username, ur.role
FROM users u
JOIN user_roles ur ON ur.user_id = u.id
WHERE u.deactivated_at IS NULL
ORDER BY u.username, ur.role;

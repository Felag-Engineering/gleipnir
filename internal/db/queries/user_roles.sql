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

-- name: ListUsersByRole :many
SELECT ur.user_id, u.username
FROM user_roles ur
JOIN users u ON u.id = ur.user_id
WHERE ur.role = :role
ORDER BY u.username;

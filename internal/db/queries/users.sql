-- name: CreateUser :one
INSERT INTO users (id, username, password_hash, created_at)
VALUES (:id, :username, :password_hash, :created_at)
RETURNING *;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = :username;

-- name: GetUser :one
SELECT * FROM users WHERE id = :id;

-- name: DeactivateUser :exec
UPDATE users SET deactivated_at = :deactivated_at WHERE id = :id;

-- name: CountUsers :one
SELECT COUNT(*) FROM users WHERE deactivated_at IS NULL;

-- name: CreateFirstUser :one
-- Atomic first-user creation: only inserts when no active users exist.
INSERT INTO users (id, username, password_hash, created_at)
SELECT :id, :username, :password_hash, :created_at
WHERE (SELECT COUNT(*) FROM users WHERE deactivated_at IS NULL) = 0
RETURNING *;

-- name: ListUsers :many
SELECT id, username, created_at, deactivated_at, slack_user_id FROM users ORDER BY created_at;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = :password_hash WHERE id = :id;

-- name: SetUserSlackUserID :exec
UPDATE users SET slack_user_id = :slack_user_id WHERE id = :id;

-- name: GetUserBySlackUserID :many
-- Returns one row per role for the active user mapped to the given slack_user_id.
-- A user with zero roles in user_roles produces no rows; such a user is treated
-- as unmapped (same rejection path as "unknown Slack id"). See plan S6.
SELECT u.id, u.username, ur.role
FROM users u
JOIN user_roles ur ON ur.user_id = u.id
WHERE u.slack_user_id = :slack_user_id
  AND u.deactivated_at IS NULL
ORDER BY ur.role;

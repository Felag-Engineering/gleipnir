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
SELECT id, username, created_at, deactivated_at FROM users ORDER BY created_at;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = :password_hash WHERE id = :id;

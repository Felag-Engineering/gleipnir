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

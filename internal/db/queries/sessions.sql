-- name: CreateSession :one
INSERT INTO sessions (id, user_id, token, created_at, expires_at)
VALUES (:id, :user_id, :token, :created_at, :expires_at)
RETURNING *;

-- name: GetSessionByToken :one
SELECT * FROM sessions WHERE token = :token;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = :id;

-- name: DeleteSessionsByUser :exec
DELETE FROM sessions WHERE user_id = :user_id;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < :now;

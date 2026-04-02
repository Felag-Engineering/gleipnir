-- name: CreateSession :one
INSERT INTO sessions (id, user_id, token, created_at, expires_at, user_agent, ip_address)
VALUES (:id, :user_id, :token, :created_at, :expires_at, :user_agent, :ip_address)
RETURNING *;

-- name: GetSessionByToken :one
SELECT * FROM sessions WHERE token = :token;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = :id;

-- name: DeleteSessionsByUser :exec
DELETE FROM sessions WHERE user_id = :user_id;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < :now;

-- name: DeleteSessionByToken :exec
DELETE FROM sessions WHERE token = :token;

-- name: ListSessionsByUser :many
SELECT id, user_id, token, user_agent, ip_address, created_at, expires_at FROM sessions WHERE user_id = :user_id AND expires_at > :now ORDER BY created_at DESC;

-- name: DeleteSessionByID :exec
DELETE FROM sessions WHERE id = :id AND user_id = :user_id;

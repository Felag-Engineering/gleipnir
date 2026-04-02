-- name: ListUserPreferences :many
SELECT * FROM user_preferences WHERE user_id = ?;

-- name: UpsertUserPreference :one
INSERT INTO user_preferences (user_id, preference_key, preference_value, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(user_id, preference_key) DO UPDATE SET preference_value = excluded.preference_value, updated_at = excluded.updated_at
RETURNING *;

-- name: DeleteUserPreference :exec
DELETE FROM user_preferences WHERE user_id = ? AND preference_key = ?;

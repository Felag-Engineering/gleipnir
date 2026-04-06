-- name: GetSystemSetting :one
SELECT key, value, updated_at FROM system_settings WHERE key = ?;

-- name: UpsertSystemSetting :exec
INSERT INTO system_settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

-- name: DeleteSystemSetting :exec
DELETE FROM system_settings WHERE key = ?;

-- name: ListSystemSettings :many
SELECT key, value, updated_at FROM system_settings ORDER BY key;

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

-- name: ListAPIKeySystemSettings :many
SELECT key, value, updated_at
FROM system_settings
WHERE key LIKE '%_api_key'
ORDER BY key;

-- name: SetSystemSettingIfEmpty :execrows
-- Writes the value only when the key is absent or holds an empty string, in
-- one statement so concurrent callers cannot both win. Returns rows affected
-- (0 = an existing non-empty value was left alone).
INSERT INTO system_settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
WHERE system_settings.value = '';

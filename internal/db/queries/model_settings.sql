-- name: GetModelSetting :one
SELECT provider, model_name, enabled, updated_at
FROM model_settings
WHERE provider = ? AND model_name = ?;

-- name: UpsertModelSetting :exec
INSERT INTO model_settings (provider, model_name, enabled, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(provider, model_name) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at;

-- name: ListModelSettings :many
SELECT provider, model_name, enabled, updated_at
FROM model_settings
ORDER BY provider, model_name;

-- name: ListEnabledModels :many
SELECT provider, model_name
FROM model_settings
WHERE enabled = 1;

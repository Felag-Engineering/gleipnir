-- name: ListOpenAICompatProviders :many
SELECT * FROM openai_compat_providers ORDER BY name ASC;

-- name: GetOpenAICompatProviderByID :one
SELECT * FROM openai_compat_providers WHERE id = :id;

-- name: GetOpenAICompatProviderByName :one
SELECT * FROM openai_compat_providers WHERE name = :name;

-- name: CreateOpenAICompatProvider :one
INSERT INTO openai_compat_providers (name, base_url, api_key_encrypted, created_at, updated_at)
VALUES (:name, :base_url, :api_key_encrypted, :created_at, :updated_at)
RETURNING *;

-- name: UpdateOpenAICompatProvider :one
UPDATE openai_compat_providers
SET name = :name,
    base_url = :base_url,
    api_key_encrypted = :api_key_encrypted,
    updated_at = :updated_at
WHERE id = :id
RETURNING *;

-- name: DeleteOpenAICompatProvider :exec
DELETE FROM openai_compat_providers WHERE id = :id;

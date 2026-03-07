-- name: CreatePolicy :one
INSERT INTO policies (id, name, trigger_type, yaml, created_at, updated_at)
VALUES (:id, :name, :trigger_type, :yaml, :created_at, :updated_at)
RETURNING *;

-- name: GetPolicy :one
SELECT * FROM policies WHERE id = :id;

-- name: GetPolicyByName :one
SELECT * FROM policies WHERE name = :name;

-- name: ListPolicies :many
SELECT * FROM policies ORDER BY created_at DESC;

-- name: UpdatePolicy :one
UPDATE policies
SET yaml = :yaml, updated_at = :updated_at
WHERE id = :id
RETURNING *;

-- name: DeletePolicy :exec
DELETE FROM policies WHERE id = :id;

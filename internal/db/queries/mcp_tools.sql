-- name: UpsertMCPTool :one
INSERT INTO mcp_tools (id, server_id, name, description, input_schema, capability_role, created_at)
VALUES (:id, :server_id, :name, :description, :input_schema, :capability_role, :created_at)
ON CONFLICT (server_id, name) DO UPDATE SET
    description     = excluded.description,
    input_schema    = excluded.input_schema,
    capability_role = excluded.capability_role
RETURNING *;

-- name: ListMCPToolsByServer :many
SELECT * FROM mcp_tools WHERE server_id = :server_id ORDER BY name ASC;

-- name: GetMCPToolByServerAndName :one
SELECT t.*
FROM mcp_tools t
JOIN mcp_servers s ON t.server_id = s.id
WHERE s.name = :server_name AND t.name = :tool_name;

-- name: DeleteMCPToolsByServer :exec
DELETE FROM mcp_tools WHERE server_id = :server_id;

-- name: GetMCPTool :one
SELECT * FROM mcp_tools WHERE id = :id;

-- name: UpdateMCPToolCapabilityRole :exec
UPDATE mcp_tools SET capability_role = :capability_role WHERE id = :id;

-- name: DeleteMCPToolByServerAndName :exec
DELETE FROM mcp_tools WHERE server_id = :server_id AND name = :name;

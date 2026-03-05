-- name: UpsertMCPTool :one
INSERT INTO mcp_tools (id, server_id, name, description, input_schema, capability_role, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (server_id, name) DO UPDATE SET
    description     = excluded.description,
    input_schema    = excluded.input_schema,
    capability_role = excluded.capability_role
RETURNING *;

-- name: ListMCPToolsByServer :many
SELECT * FROM mcp_tools WHERE server_id = ? ORDER BY name ASC;

-- name: GetMCPToolByServerAndName :one
SELECT t.*
FROM mcp_tools t
JOIN mcp_servers s ON t.server_id = s.id
WHERE s.name = ? AND t.name = ?;

-- name: DeleteMCPToolsByServer :exec
DELETE FROM mcp_tools WHERE server_id = ?;

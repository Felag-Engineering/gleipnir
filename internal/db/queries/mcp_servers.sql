-- name: CreateMCPServer :one
INSERT INTO mcp_servers (id, name, url, created_at)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetMCPServer :one
SELECT * FROM mcp_servers WHERE id = ?;

-- name: ListMCPServers :many
SELECT * FROM mcp_servers ORDER BY created_at ASC;

-- name: UpdateMCPServerLastDiscovered :exec
UPDATE mcp_servers SET last_discovered_at = ? WHERE id = ?;

-- name: DeleteMCPServer :exec
DELETE FROM mcp_servers WHERE id = ?;

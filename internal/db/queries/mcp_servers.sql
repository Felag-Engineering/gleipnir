-- name: CreateMCPServer :one
INSERT INTO mcp_servers (id, name, url, created_at)
VALUES (:id, :name, :url, :created_at)
RETURNING *;

-- name: GetMCPServer :one
SELECT * FROM mcp_servers WHERE id = :id;

-- ListMCPServers is ordered ASC: MCP servers are administrative objects registered
-- once; insertion order is the natural stable sort for configuration lists.
-- name: ListMCPServers :many
SELECT * FROM mcp_servers ORDER BY created_at ASC;

-- name: UpdateMCPServerLastDiscovered :exec
UPDATE mcp_servers SET last_discovered_at = :last_discovered_at WHERE id = :id;

-- name: UpdateMCPServerDrift :exec
UPDATE mcp_servers SET has_drift = :has_drift WHERE id = :id;

-- name: DeleteMCPServer :exec
DELETE FROM mcp_servers WHERE id = :id;

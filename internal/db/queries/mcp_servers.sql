-- name: CreateMCPServer :one
INSERT INTO mcp_servers (id, name, url, created_at, auth_headers_encrypted)
VALUES (:id, :name, :url, :created_at, :auth_headers_encrypted)
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

-- name: UpdateMCPServer :one
UPDATE mcp_servers
SET name = :name, url = :url
WHERE id = :id
RETURNING *;

-- ListMCPServersWithAuthHeaders returns only rows that have a stored ciphertext.
-- Used by rotate-key to re-encrypt all MCP auth header sets.
-- name: ListMCPServersWithAuthHeaders :many
SELECT id, auth_headers_encrypted FROM mcp_servers
WHERE auth_headers_encrypted IS NOT NULL
ORDER BY id;

-- name: UpdateMCPServerAuthHeaders :exec
UPDATE mcp_servers SET auth_headers_encrypted = :auth_headers_encrypted WHERE id = :id;

-- name: CountMCPServers :one
SELECT COUNT(*) FROM mcp_servers;

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

-- UpdateMCPServerProtocolVersion pins the negotiated MCP protocol version
-- for a registry entry. NULL clears the pin (re-probe on next discovery).
-- name: UpdateMCPServerProtocolVersion :exec
UPDATE mcp_servers SET protocol_version = :protocol_version WHERE id = :id;

-- UpdateMCPServerProtocolVersionIfNotModern conditionally pins the negotiated
-- protocol version, refusing the write when the row's current protocol_version
-- is already one of modern_versions. The guard is evaluated by SQLite against
-- the live row inside this single UPDATE, not by the caller reading the row
-- first and deciding whether to write. That read-then-write shape let two
-- concurrent refreshes both observe a stale (e.g. NULL) protocol_version and
-- race past an in-memory guard, so whichever write landed last won even if it
-- demoted a server the other goroutine had just proven modern. :execrows lets
-- the caller distinguish "the pin changed" (1 row) from "an established
-- modern pin blocked the write" (0 rows).
--
-- The condition is written as "(x IN (...)) IS NOT TRUE" rather than
-- "x NOT IN (...)" for two reasons: SQLite's three-valued logic makes IS NOT
-- TRUE correctly permit the write when protocol_version IS NULL (NULL IN (..)
-- is NULL, and NULL IS NOT TRUE is true) without a separate "OR ... IS NULL"
-- clause; and sqlc's slice-parameter rewriter (sqlc.slice) does not recognize
-- a NOT-wrapped IN clause, silently leaving the literal "sqlc.slice(...)"
-- text unexpanded in the generated query.
-- name: UpdateMCPServerProtocolVersionIfNotModern :execrows
UPDATE mcp_servers
SET protocol_version = :protocol_version
WHERE id = :id
  AND (protocol_version IN (sqlc.slice('modern_versions'))) IS NOT TRUE;

-- GetMCPServerByPluginInstance finds the registry entry backing a managed
-- plugin instance. The lookup is by instance, not by generation: a rotation
-- updates this row's url in place, so there is exactly one entry per instance
-- for its whole life.
-- name: GetMCPServerByPluginInstance :one
SELECT * FROM mcp_servers WHERE plugin_instance_id = :plugin_instance_id;

-- name: ListManagedMCPServers :many
SELECT * FROM mcp_servers WHERE plugin_instance_id IS NOT NULL ORDER BY created_at ASC;

-- CreateManagedMCPServer registers a managed plugin instance's endpoint. It is
-- separate from CreateMCPServer rather than an optional parameter on it so the
-- operator-facing create path cannot produce a managed row by accident: a
-- managed entry is created by the reconciler on a generation switch, never by
-- an admin filling in a form.
-- name: CreateManagedMCPServer :one
INSERT INTO mcp_servers (id, name, url, created_at, plugin_instance_id, protocol_version)
VALUES (:id, :name, :url, :created_at, :plugin_instance_id, :protocol_version)
RETURNING *;

-- UpdateManagedMCPServerURL repoints a managed entry at a new generation's
-- address. Guarded on plugin_instance_id so this can never move an external
-- server an operator configured.
--
-- url is part of the registry cache's invalidation key, so this update IS the
-- routing flip: the next resolve rebuilds the client against the new address,
-- while a *Client already handed to a running run keeps the old base URL and
-- drains against the generation it started on.
-- name: UpdateManagedMCPServerURL :execrows
UPDATE mcp_servers
SET url = :url
WHERE plugin_instance_id = :plugin_instance_id;

-- name: DeleteManagedMCPServer :execrows
DELETE FROM mcp_servers WHERE plugin_instance_id = :plugin_instance_id;

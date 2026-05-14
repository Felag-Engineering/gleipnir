-- name: CreatePlugin :one
INSERT INTO plugins (id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
VALUES (:id, :name, :plugin_version, :manifest_snapshot, :trusted_pubkey, :status, 0, :created_at, :updated_at)
RETURNING *;

-- name: GetPluginByID :one
SELECT * FROM plugins WHERE id = :id;

-- name: GetPluginByName :one
SELECT * FROM plugins WHERE name = :name;

-- name: ListPlugins :many
SELECT * FROM plugins ORDER BY name;

-- name: ListPluginsByStatus :many
SELECT * FROM plugins WHERE status = :status ORDER BY name;

-- UpdatePluginManifest replaces the manifest snapshot and bumps the CAS
-- version. Used when an admin approves a material manifest change
-- (ADR-045 sec 4). rows_affected == 0 means the expected_version did not
-- match (concurrent writer) -- see ADR-038.
-- name: UpdatePluginManifest :execrows
UPDATE plugins SET manifest_snapshot = :manifest_snapshot, plugin_version = :plugin_version, status = :status, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- UpdatePluginTrustedPubkey rotates the TOFU-pinned pubkey after an admin
-- accepts a new key (ADR-045 sec 2). Same CAS guard as UpdatePluginManifest.
-- name: UpdatePluginTrustedPubkey :execrows
UPDATE plugins SET trusted_pubkey = :trusted_pubkey, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- name: UpdatePluginStatus :execrows
UPDATE plugins SET status = :status, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- name: CreatePluginInstance :one
INSERT INTO plugin_instances (id, plugin_id, instance_name, config_json, subscription_scope_json, credentials_encrypted, credentials_expires_at, handshake_versions, health_state, health_detail, last_oauth_callback_url, version, created_at, updated_at)
VALUES (:id, :plugin_id, :instance_name, :config_json, :subscription_scope_json, :credentials_encrypted, :credentials_expires_at, :handshake_versions, :health_state, :health_detail, :last_oauth_callback_url, 0, :created_at, :updated_at)
RETURNING *;

-- name: GetPluginInstanceByID :one
SELECT * FROM plugin_instances WHERE id = :id;

-- name: GetPluginInstanceByName :one
SELECT * FROM plugin_instances WHERE plugin_id = :plugin_id AND instance_name = :instance_name;

-- GetPluginInstanceByGlobalName finds the first instance with the given name
-- across all plugins. Used by subscribed trigger binding to resolve the
-- operator-readable source name to an instance ID.
-- Note: plugin_instances UNIQUE is (plugin_id, instance_name), not global, so
-- cross-plugin name collisions are silently first-wins here. Revisit when #213
-- settles the instance-id pattern.
-- name: GetPluginInstanceByGlobalName :one
SELECT * FROM plugin_instances WHERE instance_name = :instance_name LIMIT 1;

-- name: ListPluginInstances :many
SELECT * FROM plugin_instances ORDER BY plugin_id, instance_name;

-- name: ListPluginInstancesByPlugin :many
SELECT * FROM plugin_instances WHERE plugin_id = :plugin_id ORDER BY instance_name;

-- name: ListPluginInstancesByHealth :many
SELECT * FROM plugin_instances WHERE health_state = :health_state ORDER BY plugin_id, instance_name;

-- UpdatePluginInstanceConfig writes a new config_json and bumps CAS version.
-- name: UpdatePluginInstanceConfig :execrows
UPDATE plugin_instances SET config_json = :config_json, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- UpdatePluginInstanceSubscriptionScope writes a new subscription_scope_json
-- and bumps the CAS version. Mirrors UpdatePluginInstanceConfig; ADR-038 CAS.
-- name: UpdatePluginInstanceSubscriptionScope :execrows
UPDATE plugin_instances SET subscription_scope_json = :subscription_scope_json, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- UpdatePluginInstanceCredentials writes new encrypted credentials. Used by
-- the admin write-only API surface and the OAuth refresh path (#227).
-- name: UpdatePluginInstanceCredentials :execrows
UPDATE plugin_instances SET credentials_encrypted = :credentials_encrypted, credentials_expires_at = :credentials_expires_at, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- UpdatePluginInstanceHealth transitions the health_state column. CAS-guarded
-- per ADR-038; the existing version column carries the lock.
-- name: UpdatePluginInstanceHealth :execrows
UPDATE plugin_instances SET health_state = :health_state, health_detail = :health_detail, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- name: UpdatePluginInstanceHandshakeVersions :execrows
UPDATE plugin_instances SET handshake_versions = :handshake_versions, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- name: UpdatePluginInstanceOAuthCallback :execrows
UPDATE plugin_instances SET last_oauth_callback_url = :last_oauth_callback_url, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- name: DeletePluginInstance :execrows
DELETE FROM plugin_instances WHERE id = :id;

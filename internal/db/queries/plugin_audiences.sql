-- name: CreatePluginAudience :one
INSERT INTO plugin_audiences (id, name, created_by_user_id, version, created_at, updated_at)
VALUES (:id, :name, :created_by_user_id, 0, :created_at, :updated_at)
RETURNING *;

-- name: GetPluginAudienceByID :one
SELECT * FROM plugin_audiences WHERE id = :id;

-- name: GetPluginAudienceByName :one
SELECT * FROM plugin_audiences WHERE name = :name;

-- name: ListPluginAudiences :many
SELECT * FROM plugin_audiences ORDER BY name;

-- UpdatePluginAudience edits the audience name and bumps the CAS version.
-- rows_affected == 0 means the expected_version did not match (concurrent
-- writer) -- see ADR-038.
-- name: UpdatePluginAudience :execrows
UPDATE plugin_audiences
SET name = :name, version = version + 1, updated_at = :updated_at
WHERE id = :id AND version = :expected_version;

-- name: DeletePluginAudience :execrows
DELETE FROM plugin_audiences WHERE id = :id;

-- GetPluginAudienceWithEntries returns the audience row joined with all its
-- entries ordered by position. An audience with zero entries returns one row
-- with NULL entry columns (LEFT JOIN). Explicit column aliases avoid any
-- ambiguity for sqlc.
-- name: GetPluginAudienceWithEntries :many
SELECT
    pa.id              AS audience_id,
    pa.name            AS audience_name,
    pa.created_by_user_id,
    pa.version         AS audience_version,
    pa.created_at      AS audience_created_at,
    pa.updated_at      AS audience_updated_at,
    ae.id              AS entry_id,
    ae.plugin_instance_id,
    ae.position,
    ae.notify,
    ae.request,
    ae.config_json
FROM plugin_audiences pa
LEFT JOIN audience_entries ae ON ae.audience_id = pa.id
WHERE pa.id = :audience_id
ORDER BY ae.position;

-- name: CreateAudienceEntry :one
INSERT INTO audience_entries (id, audience_id, plugin_instance_id, position, notify, request, config_json)
VALUES (:id, :audience_id, :plugin_instance_id, :position, :notify, :request, :config_json)
RETURNING *;

-- name: ListAudienceEntries :many
SELECT * FROM audience_entries WHERE audience_id = :audience_id ORDER BY position;

-- UpdateAudienceEntry updates the notify, request, and config_json fields of
-- an entry. Position is not changed here; use ReorderAudienceEntry for that.
-- name: UpdateAudienceEntry :execrows
UPDATE audience_entries
SET notify = :notify, request = :request, config_json = :config_json
WHERE id = :id;

-- name: DeleteAudienceEntry :execrows
DELETE FROM audience_entries WHERE id = :id;

-- ReorderAudienceEntry updates the position of a single entry. Multi-row
-- reorders inside a single transaction must use a temporary sentinel position
-- to avoid transient UNIQUE(audience_id, position) violations because
-- modernc.org/sqlite does not support DEFERRABLE on table constraints.
-- name: ReorderAudienceEntry :execrows
UPDATE audience_entries SET position = :position WHERE id = :id;

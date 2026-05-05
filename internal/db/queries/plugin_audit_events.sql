-- Plugin audit events (ADR-046).
--
-- Operator-only audit trail for plugin lifecycle. Never surfaced to the LLM.
-- All writes are host-internal -- plugins do not write here directly.

-- name: InsertPluginAuditEvent :one
INSERT INTO plugin_audit_events (
    plugin_instance_id, event_type, severity,
    actor_user_id, payload_json, created_at
) VALUES (
    :plugin_instance_id, :event_type, :severity,
    :actor_user_id, :payload_json, :created_at
)
RETURNING *;

-- name: ListPluginAuditEventsByInstance :many
SELECT * FROM plugin_audit_events
WHERE plugin_instance_id = :plugin_instance_id
ORDER BY created_at DESC, id DESC
LIMIT :limit OFFSET :offset;

-- name: ListPluginAuditEventsByType :many
SELECT * FROM plugin_audit_events
WHERE event_type = :event_type
ORDER BY created_at DESC, id DESC
LIMIT :limit OFFSET :offset;

-- ListRecentPluginAuditEvents returns the global plugin-event feed for the
-- admin UI. Severity filter is optional -- pass an empty string to disable.
-- name: ListRecentPluginAuditEvents :many
SELECT * FROM plugin_audit_events
WHERE (:severity = '' OR severity = :severity)
ORDER BY created_at DESC, id DESC
LIMIT :limit OFFSET :offset;

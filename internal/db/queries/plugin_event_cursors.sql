-- name: GetEventCursor :one
SELECT * FROM plugin_event_cursors WHERE plugin_instance_id = :plugin_instance_id;

-- name: UpsertEventCursor :exec
INSERT INTO plugin_event_cursors (plugin_instance_id, cursor, sequence, scope_hash, updated_at)
VALUES (:plugin_instance_id, :cursor, :sequence, :scope_hash, :updated_at)
ON CONFLICT (plugin_instance_id) DO UPDATE SET
    cursor     = excluded.cursor,
    sequence   = excluded.sequence,
    scope_hash = excluded.scope_hash,
    updated_at = excluded.updated_at;

-- name: ResetEventCursor :exec
-- Clears a stale-scope cursor so the next events/listen connects with no
-- cursor param, paying the redelivery cost plugin_event_dedup absorbs.
DELETE FROM plugin_event_cursors WHERE plugin_instance_id = :plugin_instance_id;

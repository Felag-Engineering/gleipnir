-- name: RecordEventIfNovel :execrows
INSERT INTO plugin_event_dedup (plugin_instance_id, event_kind, event_id, created_at_ms)
VALUES (:plugin_instance_id, :event_kind, :event_id, :created_at_ms)
ON CONFLICT (plugin_instance_id, event_kind, event_id) DO NOTHING;

-- name: SweepEventDedup :execrows
DELETE FROM plugin_event_dedup WHERE created_at_ms < :floor;

-- name: DeleteEventDedup :exec
-- Rolls back a dedup claim recorded by RecordEventIfNovel. Used when a matched
-- event failed to launch transiently, so the plugin's at-least-once redelivery
-- of the same event is treated as novel again (#585). Idempotent: a DELETE that
-- affects zero rows is not an error.
DELETE FROM plugin_event_dedup
WHERE plugin_instance_id = :plugin_instance_id
  AND event_kind = :event_kind
  AND event_id = :event_id;

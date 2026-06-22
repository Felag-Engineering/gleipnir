-- name: RecordEventIfNovel :execrows
INSERT INTO plugin_event_dedup (plugin_instance_id, event_kind, event_id, created_at_ms)
VALUES (:plugin_instance_id, :event_kind, :event_id, :created_at_ms)
ON CONFLICT (plugin_instance_id, event_kind, event_id) DO NOTHING;

-- name: SweepEventDedup :execrows
DELETE FROM plugin_event_dedup WHERE created_at_ms < :floor;

-- name: EnqueueTrigger :one
INSERT INTO trigger_queue (id, policy_id, trigger_type, trigger_payload, position, created_at)
VALUES (@id, @policy_id, @trigger_type, @trigger_payload,
        (SELECT COALESCE(MAX(position), -1) + 1 FROM trigger_queue WHERE policy_id = @policy_id),
        @created_at)
RETURNING *;

-- name: DequeueTrigger :one
DELETE FROM trigger_queue
WHERE id = (
    SELECT tq.id FROM trigger_queue tq
    WHERE tq.policy_id = @policy_id
    ORDER BY tq.position ASC
    LIMIT 1
)
RETURNING *;

-- name: CountQueuedTriggers :one
SELECT COUNT(*) FROM trigger_queue WHERE policy_id = @policy_id;

-- name: RequeueTriggerAtFront :one
INSERT INTO trigger_queue (id, policy_id, trigger_type, trigger_payload, position, created_at)
VALUES (@id, @policy_id, @trigger_type, @trigger_payload,
        (SELECT COALESCE(MIN(position), 1) - 1 FROM trigger_queue WHERE policy_id = @policy_id),
        @created_at)
RETURNING *;

-- name: DeleteQueuedTriggersByPolicy :exec
DELETE FROM trigger_queue WHERE policy_id = @policy_id;

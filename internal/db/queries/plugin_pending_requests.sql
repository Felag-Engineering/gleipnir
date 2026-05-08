-- name: CreatePluginPendingRequest :one
INSERT INTO plugin_pending_requests (id, plugin_instance_id, run_id, audience_entry_id, tool_name, status, expires_at, created_at)
VALUES (:id, :plugin_instance_id, :run_id, :audience_entry_id, :tool_name, 'pending', :expires_at, :created_at)
RETURNING *;

-- name: GetPluginPendingRequest :one
SELECT * FROM plugin_pending_requests WHERE id = :id;

-- name: GetPendingPluginRequestsByRun :many
SELECT * FROM plugin_pending_requests WHERE run_id = :run_id AND status = 'pending';

-- UpdatePluginPendingRequestStatus transitions a pending request to a terminal
-- status. The WHERE clause guards against double-transition: rows_affected == 0
-- means another writer already resolved the request.
-- name: UpdatePluginPendingRequestStatus :execrows
UPDATE plugin_pending_requests
SET status = :status, response = :response, resolved_at = :resolved_at
WHERE id = :id AND status = 'pending';

-- ListExpiredPluginPendingRequests returns all pending plugin requests whose
-- expires_at is at or before the cutoff. Only rows with a non-NULL expires_at
-- are candidates (rows without a timeout are excluded).
-- name: ListExpiredPluginPendingRequests :many
SELECT id, run_id, tool_name FROM plugin_pending_requests
WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at <= :cutoff;

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

-- ListPendingPluginRequestsByAudience returns all pending requests whose
-- audience_entry_id belongs to the given audience. Used by the /references
-- endpoint to surface in-flight runs for a specific audience.
-- name: ListPendingPluginRequestsByAudience :many
SELECT p.id, p.run_id, p.audience_entry_id, p.status, r.status AS run_status
FROM plugin_pending_requests p
JOIN audience_entries ae ON ae.id = p.audience_entry_id
JOIN runs r ON r.id = p.run_id
WHERE ae.audience_id = :audience_id AND p.status = 'pending';

-- ListAudienceIDsWithPendingRequests returns the distinct audience IDs that
-- currently have at least one pending plugin request. Used for bulk
-- has_in_flight_runs on the list endpoint (no N+1).
-- name: ListAudienceIDsWithPendingRequests :many
SELECT DISTINCT ae.audience_id
FROM plugin_pending_requests p
JOIN audience_entries ae ON ae.id = p.audience_entry_id
WHERE p.status = 'pending';

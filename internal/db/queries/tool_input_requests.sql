-- name: CreateToolInputRequest :one
INSERT INTO tool_input_requests (id, run_id, server_id, tool_name, call_args, request_state, request_payload, elicitation_kind, status, expires_at, deadline_source, created_at)
VALUES (:id, :run_id, :server_id, :tool_name, :call_args, :request_state, :request_payload, :elicitation_kind, 'pending', :expires_at, :deadline_source, :created_at)
RETURNING *;

-- name: GetToolInputRequest :one
SELECT * FROM tool_input_requests WHERE id = :id;

-- ResolveToolInputRequest transitions a pending tool input request to resolved
-- once the operator has answered. The WHERE clause guards against
-- double-transition: rows_affected == 0 means another writer already
-- resolved or expired the request.
-- name: ResolveToolInputRequest :execrows
UPDATE tool_input_requests
SET status = 'resolved', response = :response, resolved_at = :resolved_at
WHERE id = :id AND status = 'pending';

-- ExpireToolInputRequest transitions a pending tool input request to
-- timed_out when Gleipnir's policy timeout elapses before the operator
-- responds (spec sec 6.3: Gleipnir's timeout is authoritative for the human leg).
-- name: ExpireToolInputRequest :execrows
UPDATE tool_input_requests
SET status = 'timed_out', resolved_at = :resolved_at
WHERE id = :id AND status = 'pending';

-- ListResumableToolInputRequests returns every pending tool input request so
-- the host can re-arm its wait state after a restart (spec sec 13: persisted
-- requestState survives restarts even though full run resurrection does not).
-- name: ListResumableToolInputRequests :many
SELECT * FROM tool_input_requests WHERE status = 'pending';

-- GetPendingToolInputRequestsByRun returns the pending tool input requests for
-- one run, oldest first. The resolution endpoint uses it to find what an
-- operator is answering; a run pauses on one request at a time, so in practice
-- this returns zero or one row, and zero means there is no active gate.
-- name: GetPendingToolInputRequestsByRun :many
SELECT * FROM tool_input_requests
WHERE run_id = :run_id AND status = 'pending'
ORDER BY created_at;

-- ListExpiredToolInputRequests returns pending tool input requests whose
-- effective deadline has passed, for the timeout scanner. Without this the row
-- of a run paused when the host died would stay pending forever: the in-process
-- timer that would have expired it died with the process.
-- name: ListExpiredToolInputRequests :many
SELECT * FROM tool_input_requests
WHERE status = 'pending' AND expires_at <= :cutoff;

-- UpdateToolInputRequestDeadline extends or shortens a pending request's
-- effective deadline, for when a server revises its task TTL mid-wait
-- (spec sec 6.3). Guarded on status so a resolved or timed-out request cannot
-- be given a new lease.
-- name: UpdateToolInputRequestDeadline :execrows
UPDATE tool_input_requests
SET expires_at = :expires_at, deadline_source = :deadline_source
WHERE id = :id AND status = 'pending';

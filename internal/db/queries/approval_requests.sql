-- name: CreateApprovalRequest :one
INSERT INTO approval_requests (id, run_id, tool_name, proposed_input, reasoning_summary, status, expires_at, created_at)
VALUES (:id, :run_id, :tool_name, :proposed_input, :reasoning_summary, 'pending', :expires_at, :created_at)
RETURNING *;

-- name: GetApprovalRequest :one
SELECT * FROM approval_requests WHERE id = :id;

-- name: ListPendingApprovalRequests :many
SELECT * FROM approval_requests WHERE status = 'pending' ORDER BY created_at ASC;

-- ListExpiredApprovalRequests accepts a cutoff timestamp and returns all pending
-- requests whose expires_at is at or before that value. Callers typically pass
-- time.Now() but can pass an earlier value to query historical expiry state.
-- name: ListExpiredApprovalRequests :many
SELECT * FROM approval_requests WHERE status = 'pending' AND expires_at <= :cutoff;

-- UpdateApprovalRequestStatus: caller is responsible for valid state transitions
-- (pending -> approved / rejected / timeout). The schema CHECK constraint
-- enforces enum membership but not transition ordering.
-- name: UpdateApprovalRequestStatus :exec
UPDATE approval_requests
SET status = :status, decided_at = :decided_at, note = :note
WHERE id = :id;

-- name: GetPendingApprovalRequestsByRun :many
SELECT * FROM approval_requests
WHERE run_id = :run_id AND status = 'pending'
ORDER BY created_at ASC;

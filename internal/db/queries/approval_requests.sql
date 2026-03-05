-- name: CreateApprovalRequest :one
INSERT INTO approval_requests (id, run_id, tool_name, proposed_input, reasoning_summary, status, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)
RETURNING *;

-- name: GetApprovalRequest :one
SELECT * FROM approval_requests WHERE id = ?;

-- name: ListPendingApprovalRequests :many
SELECT * FROM approval_requests WHERE status = 'pending' ORDER BY created_at ASC;

-- name: ListExpiredApprovalRequests :many
SELECT * FROM approval_requests WHERE status = 'pending' AND expires_at <= ?;

-- name: UpdateApprovalRequestStatus :exec
UPDATE approval_requests
SET status = ?, decided_at = ?, note = ?
WHERE id = ?;

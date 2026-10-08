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

-- UpdateApprovalRequestStatus transitions a pending approval to a terminal status.
-- The WHERE clause guards against double-transition: if another writer (scanner
-- or agent timer) already resolved the request, rows_affected == 0 and the
-- caller must treat that as "already resolved, skip downstream side-effects".
-- name: UpdateApprovalRequestStatus :execrows
UPDATE approval_requests
SET status = :status, decided_at = :decided_at, note = :note, decided_by = :decided_by
WHERE id = :id AND status = 'pending';

-- name: GetPendingApprovalRequestsByRun :many
SELECT * FROM approval_requests
WHERE run_id = :run_id AND status = 'pending'
ORDER BY created_at ASC;

-- name: CountPendingApprovalRequests :one
SELECT COUNT(*) FROM approval_requests WHERE status = 'pending';

-- ListApprovalDecidersByRun returns each approval of a run with the username of
-- the user who decided it. decided_by is NULL for a timeout, a row that predates
-- the column, or a since-deleted account; LEFT JOIN keeps those rows.
-- name: ListApprovalDecidersByRun :many
SELECT ar.id, ar.status, ar.decided_at, ar.decided_by, u.username AS decided_by_username
FROM approval_requests ar
LEFT JOIN users u ON u.id = ar.decided_by
WHERE ar.run_id = :run_id
ORDER BY ar.created_at ASC;

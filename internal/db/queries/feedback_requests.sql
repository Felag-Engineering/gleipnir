-- name: CreateFeedbackRequest :one
INSERT INTO feedback_requests (id, run_id, tool_name, proposed_input, message, status, expires_at, created_at)
VALUES (:id, :run_id, :tool_name, :proposed_input, :message, 'pending', :expires_at, :created_at)
RETURNING *;

-- name: GetFeedbackRequest :one
SELECT * FROM feedback_requests WHERE id = :id;

-- name: GetPendingFeedbackRequestsByRun :many
SELECT * FROM feedback_requests WHERE run_id = :run_id AND status = 'pending';

-- UpdateFeedbackRequestStatus transitions a pending feedback request to a terminal
-- status. The WHERE clause guards against double-transition: rows_affected == 0
-- means another writer already resolved the request.
-- name: UpdateFeedbackRequestStatus :execrows
UPDATE feedback_requests
SET status = :status, response = :response, resolved_at = :resolved_at, responded_by = :responded_by
WHERE id = :id AND status = 'pending';

-- name: CountPendingFeedbackRequests :one
SELECT COUNT(*) FROM feedback_requests WHERE status = 'pending';

-- ListExpiredFeedbackRequests returns all pending feedback requests whose
-- expires_at is at or before the cutoff. Only rows with a non-NULL expires_at
-- are candidates (old rows without timeout are excluded).
-- name: ListExpiredFeedbackRequests :many
SELECT * FROM feedback_requests WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at <= :cutoff;

-- ListFeedbackRespondersByRun mirrors ListApprovalDecidersByRun for feedback.
-- name: ListFeedbackRespondersByRun :many
SELECT fr.id, fr.status, fr.resolved_at, fr.responded_by, u.username AS responded_by_username
FROM feedback_requests fr
LEFT JOIN users u ON u.id = fr.responded_by
WHERE fr.run_id = :run_id
ORDER BY fr.created_at ASC;

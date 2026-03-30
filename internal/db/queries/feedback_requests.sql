-- name: CreateFeedbackRequest :one
INSERT INTO feedback_requests (id, run_id, tool_name, proposed_input, message, status, created_at)
VALUES (:id, :run_id, :tool_name, :proposed_input, :message, 'pending', :created_at)
RETURNING *;

-- name: GetFeedbackRequest :one
SELECT * FROM feedback_requests WHERE id = :id;

-- name: GetPendingFeedbackRequestsByRun :many
SELECT * FROM feedback_requests WHERE run_id = :run_id AND status = 'pending';

-- name: UpdateFeedbackRequestStatus :exec
UPDATE feedback_requests
SET status = :status, response = :response, resolved_at = :resolved_at
WHERE id = :id;

-- name: CountPendingFeedbackRequests :one
SELECT COUNT(*) FROM feedback_requests WHERE status = 'pending';

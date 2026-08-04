-- name: CreateMCPTask :one
INSERT INTO mcp_tasks (id, run_id, server_id, task_id, kind, poll_interval_ms, server_ttl, status, created_at, updated_at)
VALUES (:id, :run_id, :server_id, :task_id, :kind, :poll_interval_ms, :server_ttl, 'working', :created_at, :updated_at)
RETURNING *;

-- name: GetMCPTask :one
SELECT * FROM mcp_tasks WHERE id = :id;

-- ResolveMCPTask transitions a non-terminal task (working or input_required)
-- to a terminal status (complete, failed, or cancelled) and records its
-- result. The WHERE clause guards against double-transition: rows_affected
-- == 0 means another writer already resolved or expired the task.
-- name: ResolveMCPTask :execrows
UPDATE mcp_tasks
SET status = :status, result = :result, updated_at = :updated_at
WHERE id = :id AND status IN ('working', 'input_required');

-- ExpireMCPTask marks a task expired when the server-side TTL elapses before
-- the task reaches a terminal state on its own (spec sec 6.5: server TTL
-- expiry is surfaced as a distinct failure, not conflated with a task the
-- server itself failed).
-- name: ExpireMCPTask :execrows
UPDATE mcp_tasks
SET status = 'expired', updated_at = :updated_at
WHERE id = :id AND status IN ('working', 'input_required');

-- ListResumableMCPTasks returns every task not yet in a terminal state so the
-- host can resume polling after a restart (spec sec 13 durability claim).
-- name: ListResumableMCPTasks :many
SELECT * FROM mcp_tasks WHERE status IN ('working', 'input_required');

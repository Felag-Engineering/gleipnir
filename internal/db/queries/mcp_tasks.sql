-- name: CreateMCPTask :one
INSERT INTO mcp_tasks (id, run_id, server_id, task_id, kind, poll_interval_ms, server_ttl, status, created_at, updated_at)
VALUES (:id, :run_id, :server_id, :task_id, :kind, :poll_interval_ms, :server_ttl, 'working', :created_at, :updated_at)
RETURNING *;

-- name: GetMCPTask :one
SELECT * FROM mcp_tasks WHERE id = :id;

-- GetMCPTaskByServerAndTaskID resolves a task by the SERVER's own task_id,
-- scoped to one mcp_servers row (the UNIQUE(server_id, task_id) constraint
-- this table already carries). Used by host/authorize_actor's poll-now hint
-- (internal/plugin/hostendpoint/authorize.go, issue #961 review item 6): a
-- plugin's AuthorizeActor call names the task by ITS OWN id, not by
-- mcp_tasks.id, and scoping the lookup to the calling instance's own server
-- row is what stops one instance's task_id string from ever resolving to a
-- different instance's row.
-- name: GetMCPTaskByServerAndTaskID :one
SELECT * FROM mcp_tasks WHERE server_id = :server_id AND task_id = :task_id;

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

-- name: CreateRunStep :one
INSERT INTO run_steps (id, run_id, step_number, type, content, token_cost, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListRunSteps :many
SELECT * FROM run_steps WHERE run_id = ? ORDER BY step_number ASC;

-- name: CountRunSteps :one
SELECT COUNT(*) FROM run_steps WHERE run_id = ?;

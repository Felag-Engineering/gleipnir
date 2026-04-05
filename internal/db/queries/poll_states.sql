-- name: UpsertPollState :exec
INSERT INTO poll_states (policy_id, last_poll_at, last_result_hash, consecutive_failures, next_poll_at, created_at, updated_at)
VALUES (:policy_id, :last_poll_at, :last_result_hash, :consecutive_failures, :next_poll_at, :created_at, :updated_at)
ON CONFLICT(policy_id) DO UPDATE SET
    last_poll_at = excluded.last_poll_at,
    last_result_hash = excluded.last_result_hash,
    consecutive_failures = excluded.consecutive_failures,
    next_poll_at = excluded.next_poll_at,
    updated_at = excluded.updated_at;

-- name: GetPollState :one
SELECT * FROM poll_states WHERE policy_id = :policy_id;

-- name: DeletePollState :exec
DELETE FROM poll_states WHERE policy_id = :policy_id;

-- name: InsertPluginOAuthNonce :exec
INSERT INTO plugin_oauth_nonces (nonce, instance_id, expires_at, created_at)
VALUES (:nonce, :instance_id, :expires_at, :created_at);

-- ConsumePluginOAuthNonce atomically deletes the nonce and returns its
-- instance_id and expires_at. Returns ErrNoRows when the nonce is unknown
-- or has already been consumed.
-- name: ConsumePluginOAuthNonce :one
DELETE FROM plugin_oauth_nonces WHERE nonce = :nonce RETURNING instance_id, expires_at;

-- name: PrunePluginOAuthNonces :exec
DELETE FROM plugin_oauth_nonces WHERE expires_at < :cutoff;

-- DeletePluginOAuthNoncesByInstance removes all OAuth nonce rows for a given
-- instance. Called before deleting the instance row so there are no dangling
-- references.
-- name: DeletePluginOAuthNoncesByInstance :exec
DELETE FROM plugin_oauth_nonces WHERE instance_id = ?;

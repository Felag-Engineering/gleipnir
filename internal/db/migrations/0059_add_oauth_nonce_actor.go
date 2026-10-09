package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddOAuthNonceActor adds plugin_oauth_nonces.actor_user_id (issue #686): the
// admin who started an authcode flow. The unauthenticated OAuth callback reads
// it back when it consumes the nonce, so the issued-token audit event is
// attributed to that admin without the user id ever travelling in the `state`
// parameter. NULL means the flow predates the column or was system-started.
// ON DELETE SET NULL keeps an in-flight nonce usable if the account is removed.
type AddOAuthNonceActor struct{}

func (m *AddOAuthNonceActor) Version() int { return 49 }
func (m *AddOAuthNonceActor) Name() string { return "add_oauth_nonce_actor" }

func (m *AddOAuthNonceActor) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	return tableHasColumn(ctx, db, "plugin_oauth_nonces", "actor_user_id")
}

func (m *AddOAuthNonceActor) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE plugin_oauth_nonces ADD COLUMN actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL`,
	); err != nil {
		return fmt.Errorf("add plugin_oauth_nonces.actor_user_id: %w", err)
	}
	slog.Info("migrated: added actor_user_id to plugin_oauth_nonces")
	return nil
}

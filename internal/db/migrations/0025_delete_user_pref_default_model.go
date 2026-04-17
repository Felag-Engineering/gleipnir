package migrations

import (
	"context"
	"database/sql"
	"log/slog"
)

// DeleteUserPrefDefaultModel removes any user_preferences rows with key =
// 'default_model'. Default-model resolution is now driven entirely by the
// admin-managed system setting; per-user overrides are no longer supported.
// The DELETE is inherently idempotent — re-running it after all rows are gone
// is a safe no-op.
type DeleteUserPrefDefaultModel struct{}

func (m *DeleteUserPrefDefaultModel) Version() int { return 14 }
func (m *DeleteUserPrefDefaultModel) Name() string { return "delete_user_pref_default_model" }

func (m *DeleteUserPrefDefaultModel) Up(ctx context.Context, tx *sql.Tx) error {
	result, err := tx.ExecContext(ctx,
		`DELETE FROM user_preferences WHERE preference_key = 'default_model'`,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	slog.InfoContext(ctx, "migrated: deleted default_model user preferences", "rows_deleted", n)
	return nil
}

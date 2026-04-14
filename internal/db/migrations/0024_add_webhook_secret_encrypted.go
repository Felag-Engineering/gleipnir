package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AddWebhookSecretEncrypted adds the webhook_secret_encrypted column to the
// policies table on existing deployments. New deployments get it from
// 0001_initial.sql; this migration is a no-op for them (ShouldSkip returns true
// when the column is already present).
type AddWebhookSecretEncrypted struct{}

func (m *AddWebhookSecretEncrypted) Version() int { return 13 }
func (m *AddWebhookSecretEncrypted) Name() string { return "add_webhook_secret_encrypted" }

func (m *AddWebhookSecretEncrypted) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(policies)`)
	if err != nil {
		return false, fmt.Errorf("pragma table_info(policies): %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("scan table_info row: %w", err)
		}
		if strings.EqualFold(name, "webhook_secret_encrypted") {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (m *AddWebhookSecretEncrypted) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE policies ADD COLUMN webhook_secret_encrypted TEXT;`,
	); err != nil {
		return fmt.Errorf("add webhook_secret_encrypted column: %w", err)
	}
	slog.Info("migrated: added policies.webhook_secret_encrypted")
	return nil
}

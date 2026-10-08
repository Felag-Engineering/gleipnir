package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddPolicyArchive adds policies.deleted_at so deleting an agent archives it
// instead of cascading away its run history (issue #1052), and replaces the
// table-level UNIQUE on policies.name with a partial unique index over live
// (non-archived) rows so an archived name can be reused.
//
// SQLite cannot drop a column-level UNIQUE constraint, so this uses the
// table-recreation pattern (same as 0032). Column lists are explicit rather
// than SELECT * because the column order differs between fresh and upgraded
// databases (webhook_secret_encrypted position). PRAGMA foreign_keys must be
// toggled outside the transaction; with it off, DROP TABLE policies does not
// cascade into runs / trigger_queue / poll_states.
type AddPolicyArchive struct{}

func (m *AddPolicyArchive) Version() int { return 47 }
func (m *AddPolicyArchive) Name() string { return "add_policy_archive" }

func (m *AddPolicyArchive) RequiresForeignKeysOff() bool { return true }

func (m *AddPolicyArchive) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_policies_name_active'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("inspect policies indexes: %w", err)
	}
	return count > 0, nil
}

func (m *AddPolicyArchive) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE policies_new (
    id                        TEXT PRIMARY KEY,
    name                      TEXT NOT NULL,
    trigger_type              TEXT NOT NULL CHECK(trigger_type IN ('webhook','manual','scheduled','poll','cron','subscribed')),
    yaml                      TEXT NOT NULL,
    created_at                TEXT NOT NULL,
    updated_at                TEXT NOT NULL,
    paused_at                 TEXT,
    webhook_secret_encrypted  TEXT,
    deleted_at                TEXT
);
INSERT INTO policies_new (id, name, trigger_type, yaml, created_at, updated_at, paused_at, webhook_secret_encrypted)
SELECT id, name, trigger_type, yaml, created_at, updated_at, paused_at, webhook_secret_encrypted FROM policies;
DROP TABLE policies;
ALTER TABLE policies_new RENAME TO policies;
CREATE INDEX idx_policies_trigger_type ON policies(trigger_type);
CREATE UNIQUE INDEX idx_policies_name_active ON policies(name) WHERE deleted_at IS NULL;`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("recreate policies with deleted_at: %w", err)
	}

	slog.Info("migrated: added policies.deleted_at and partial unique name index")
	return nil
}

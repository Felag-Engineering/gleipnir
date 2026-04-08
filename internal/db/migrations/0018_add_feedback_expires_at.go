package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddFeedbackExpiresAt adds the expires_at column to feedback_requests and
// updates the status CHECK constraint to include 'timed_out'. New deployments
// get both from 0001_initial.sql; this migration is a no-op for them.
//
// SQLite does not support ALTER COLUMN, so we use the table-recreation pattern.
// PRAGMA foreign_keys must be set OUTSIDE the transaction (SQLite requirement —
// see AddWaitingForFeedback for the same pattern).
type AddFeedbackExpiresAt struct{}

func (m *AddFeedbackExpiresAt) Version() int { return 7 }
func (m *AddFeedbackExpiresAt) Name() string { return "add_feedback_expires_at" }

func (m *AddFeedbackExpiresAt) RequiresForeignKeysOff() bool { return true }

func (m *AddFeedbackExpiresAt) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(feedback_requests)")
	if err != nil {
		return false, fmt.Errorf("pragma table_info feedback_requests: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "expires_at" {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (m *AddFeedbackExpiresAt) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE feedback_requests_new (
    id              TEXT    PRIMARY KEY,
    run_id          TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    tool_name       TEXT    NOT NULL,
    proposed_input  TEXT    NOT NULL,
    message         TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK(status IN ('pending', 'resolved', 'timed_out')),
    response        TEXT,
    resolved_at     TEXT,
    expires_at      TEXT,
    created_at      TEXT    NOT NULL
);
INSERT INTO feedback_requests_new (id, run_id, tool_name, proposed_input, message, status, response, resolved_at, expires_at, created_at)
    SELECT id, run_id, tool_name, proposed_input, message, status, response, resolved_at, NULL, created_at
    FROM feedback_requests;
DROP TABLE feedback_requests;
ALTER TABLE feedback_requests_new RENAME TO feedback_requests;
CREATE INDEX idx_feedback_requests_run_id         ON feedback_requests(run_id);
CREATE INDEX idx_feedback_requests_status         ON feedback_requests(status);
CREATE INDEX idx_feedback_requests_run_pending    ON feedback_requests(run_id, status);
CREATE INDEX idx_feedback_requests_status_expires ON feedback_requests(status, expires_at);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("recreate feedback_requests with expires_at: %w", err)
	}

	slog.Info("migrated feedback_requests table to add expires_at and timed_out status")
	return nil
}

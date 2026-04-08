package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddFeedbackRequests creates the feedback_requests table and its indexes on
// existing deployments. New deployments get it from 0001_initial.sql; this
// migration is a no-op for them.
type AddFeedbackRequests struct{}

func (m *AddFeedbackRequests) Version() int { return 5 }
func (m *AddFeedbackRequests) Name() string { return "add_feedback_requests" }

func (m *AddFeedbackRequests) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='feedback_requests'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check feedback_requests existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddFeedbackRequests) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE feedback_requests (
    id              TEXT    PRIMARY KEY,
    run_id          TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    tool_name       TEXT    NOT NULL,
    proposed_input  TEXT    NOT NULL,
    message         TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK(status IN ('pending', 'resolved')),
    response        TEXT,
    resolved_at     TEXT,
    created_at      TEXT    NOT NULL
);
CREATE INDEX idx_feedback_requests_run_id         ON feedback_requests(run_id);
CREATE INDEX idx_feedback_requests_status         ON feedback_requests(status);
CREATE INDEX idx_feedback_requests_run_pending    ON feedback_requests(run_id, status);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create feedback_requests: %w", err)
	}

	slog.Info("migrated: created feedback_requests table")
	return nil
}

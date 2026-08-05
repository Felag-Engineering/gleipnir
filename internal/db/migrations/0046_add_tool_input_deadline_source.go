package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddToolInputDeadlineSource adds tool_input_requests.deadline_source, which
// records WHICH clock produced the row's effective deadline (ADR-055,
// mcp-realignment-spec.md §6.3).
//
// expires_at already stores the effective deadline — the minimum of Gleipnir's
// policy timeout, the server's task TTL, and any TTL the server declares for
// its opaque requestState. What it cannot store is which of those won, and that
// distinction is the difference between two unrelated failures: "no human
// answered in time" (look at your operators, or your timeout) and "the server
// discarded the state needed to accept an answer" (look at the server, and
// consider replaying the answer against a fresh request).
//
// The column is nullable rather than defaulted because a NULL genuinely means
// "written before this column existed", which is not the same as "the policy
// clock won". Readers treat NULL as unknown.
type AddToolInputDeadlineSource struct{}

func (m *AddToolInputDeadlineSource) Version() int { return 36 }
func (m *AddToolInputDeadlineSource) Name() string {
	return "add_tool_input_deadline_source"
}

func (m *AddToolInputDeadlineSource) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(tool_input_requests)`)
	if err != nil {
		return false, fmt.Errorf("inspect tool_input_requests columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan tool_input_requests column: %w", err)
		}
		if name == "deadline_source" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate tool_input_requests columns: %w", err)
	}
	return false, nil
}

func (m *AddToolInputDeadlineSource) Up(ctx context.Context, tx *sql.Tx) error {
	// ADD COLUMN with a CHECK is accepted by SQLite for a nullable column
	// because the constraint is evaluated per row on write; existing rows hold
	// NULL, which satisfies it.
	const ddl = `
ALTER TABLE tool_input_requests
ADD COLUMN deadline_source TEXT
CHECK(deadline_source IS NULL OR deadline_source IN ('policy', 'server_ttl', 'request_state'));`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add tool_input_requests.deadline_source: %w", err)
	}

	slog.Info("migrated: added deadline_source to tool_input_requests")
	return nil
}

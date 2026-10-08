package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddDeciderIdentity adds approval_requests.decided_by and
// feedback_requests.responded_by (issue #684): the user who settled the
// request. NULL means the system settled it (timeout) or the row predates the
// column. ON DELETE SET NULL keeps the decision record when an account is
// hard-deleted; SQLite allows ADD COLUMN with REFERENCES only because the
// default is NULL.
type AddDeciderIdentity struct{}

func (m *AddDeciderIdentity) Version() int { return 48 }
func (m *AddDeciderIdentity) Name() string { return "add_decider_identity" }

func (m *AddDeciderIdentity) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	approvalHas, err := tableHasColumn(ctx, db, "approval_requests", "decided_by")
	if err != nil {
		return false, err
	}
	feedbackHas, err := tableHasColumn(ctx, db, "feedback_requests", "responded_by")
	if err != nil {
		return false, err
	}
	return approvalHas && feedbackHas, nil
}

func (m *AddDeciderIdentity) Up(ctx context.Context, tx *sql.Tx) error {
	stmts := []struct{ desc, ddl string }{
		{"approval_requests.decided_by", `ALTER TABLE approval_requests ADD COLUMN decided_by TEXT REFERENCES users(id) ON DELETE SET NULL`},
		{"feedback_requests.responded_by", `ALTER TABLE feedback_requests ADD COLUMN responded_by TEXT REFERENCES users(id) ON DELETE SET NULL`},
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s.ddl); err != nil {
			return fmt.Errorf("add %s: %w", s.desc, err)
		}
	}
	slog.Info("migrated: added decided_by to approval_requests and responded_by to feedback_requests")
	return nil
}

func tableHasColumn(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
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
			return false, fmt.Errorf("scan %s column: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate %s columns: %w", table, err)
	}
	return false, nil
}

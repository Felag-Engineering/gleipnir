package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddToolInputReplayContext adds tool_input_requests.replay_context, which
// records what an operator was asked and answered BEFORE the request they are
// now looking at (ADR-055, mcp-realignment-spec.md §6.5).
//
// It is written only on the re-prompt path: the server's MRTR state expired,
// the host re-issued the original call, and the fresh question was NOT
// identical to the answered one. (When it is identical the answer is replayed
// silently and no second row exists at all.) Without this column the operator
// sees what looks like a duplicate prompt with no way to tell that the
// question changed underneath their previous answer — the precise situation in
// which a reflexive approval does the most damage.
//
// Nullable because the overwhelming majority of rows are first asks, and a
// NULL is meaningfully different from an empty context: it means "nothing
// preceded this", not "something preceded it and was empty".
type AddToolInputReplayContext struct{}

func (m *AddToolInputReplayContext) Version() int { return 37 }
func (m *AddToolInputReplayContext) Name() string {
	return "add_tool_input_replay_context"
}

func (m *AddToolInputReplayContext) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
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
		if name == "replay_context" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate tool_input_requests columns: %w", err)
	}
	return false, nil
}

func (m *AddToolInputReplayContext) Up(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
ALTER TABLE tool_input_requests
ADD COLUMN replay_context TEXT;`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add tool_input_requests.replay_context: %w", err)
	}

	slog.Info("migrated: added replay_context to tool_input_requests")
	return nil
}

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddPluginAuditRunID adds plugin_audit_events.run_id so a decision record can
// be found by the run it belongs to (ADR-055, mcp-realignment-spec.md §6.6;
// issue #803).
//
// Until now every row in this table was about a plugin INSTANCE — installed,
// rotated, refused a call — and the instance ID was the only handle anyone
// needed. A decision record is the first row type that is about a RUN: what a
// human was asked mid-execution, who answered, through which channel, and how
// strongly that channel authenticates the person it names.
//
// Those records must not go in run_steps. ADR-046 splits the two tables on
// exactly this line: run_steps is replayed into the model's context, so
// anything written there is something the agent reads. The agent-visible trace
// of a paused-and-resumed call is tool_call → tool_result and nothing else —
// the human exchange is oversight evidence, not reasoning input. Putting it in
// context would also hand a server a channel for smuggling instructions
// through an operator's own answer.
//
// The column is nullable because the overwhelming majority of rows here have
// no run: a plugin installing itself is not part of anyone's run. ON DELETE
// SET NULL rather than CASCADE, matching plugin_instance_id: deleting a run
// must not delete the evidence that a human approved something during it —
// that is precisely the record an audit would want to still exist.
type AddPluginAuditRunID struct{}

func (m *AddPluginAuditRunID) Version() int { return 39 }
func (m *AddPluginAuditRunID) Name() string { return "add_plugin_audit_run_id" }

func (m *AddPluginAuditRunID) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(plugin_audit_events)`)
	if err != nil {
		return false, fmt.Errorf("inspect plugin_audit_events columns: %w", err)
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
			return false, fmt.Errorf("scan plugin_audit_events column: %w", err)
		}
		if name == "run_id" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate plugin_audit_events columns: %w", err)
	}
	return false, nil
}

func (m *AddPluginAuditRunID) Up(ctx context.Context, tx *sql.Tx) error {
	// ADD COLUMN with a REFERENCES clause is legal in SQLite as long as the
	// new column defaults to NULL, which is what makes this an in-place add
	// rather than the table-rebuild dance.
	const ddl = `
ALTER TABLE plugin_audit_events
ADD COLUMN run_id TEXT REFERENCES runs(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_pae_run_created ON plugin_audit_events(run_id, created_at);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add plugin_audit_events.run_id: %w", err)
	}

	slog.Info("migrated: added run_id to plugin_audit_events")
	return nil
}

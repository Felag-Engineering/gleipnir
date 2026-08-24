package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddPluginEventCursors creates the plugin_event_cursors table on existing
// deployments. New deployments get it from 0001_initial.sql; this migration
// is a no-op for them (ShouldSkip detects the existing table).
//
// This is the durable listen-cursor store for the io.gleipnir/events
// extension (ADR-054, mcp-realignment-spec.md §5). events/listen has no
// in-band ack — the ack IS the cursor sent on the next (re)connect's cursor
// param (extension doc §7.3) — so this table is the entire acknowledgement
// mechanism. It must be written AFTER the dispatcher has consumed an event,
// never on receipt: advancing on receipt would silently convert the
// extension's at-least-once delivery into at-most-once across a restart,
// which is exactly the failure mode dedup exists to absorb, not create.
//
// One row per plugin instance (PRIMARY KEY, not a composite key) because a
// listener holds exactly one open events/listen stream at a time.
//
// scope_hash exists because a cursor is only meaningful under the
// subscription it was earned against: a resume token from a listen call for
// kinds={A,B} does not mean the same thing when scope changes to kinds={A}.
// A helpful server that "resumed" a stale-scope cursor would hand back a
// filtered replay that looks correct and silently omits everything the new
// scope added. On a scope change the host resets to an empty cursor (see
// ResetEventCursor in internal/db/queries/plugin_event_cursors.sql) and pays
// the redelivery cost, which the existing plugin_event_dedup store absorbs.
//
// sequence records the last consumed gleipnirseq purely for diagnostics and
// the monotonicity check in internal/plugin/events: it is never sent on the
// wire (the opaque cursor string is what travels in events/listen's cursor
// param).
type AddPluginEventCursors struct{}

func (m *AddPluginEventCursors) Version() int { return 41 }
func (m *AddPluginEventCursors) Name() string { return "add_plugin_event_cursors" }

func (m *AddPluginEventCursors) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_event_cursors'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check plugin_event_cursors existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddPluginEventCursors) Up(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE plugin_event_cursors (
    plugin_instance_id TEXT    PRIMARY KEY REFERENCES plugin_instances(id) ON DELETE CASCADE,
    cursor             TEXT    NOT NULL,  -- opaque server-issued resume token
    sequence           INTEGER NOT NULL,  -- last consumed gleipnirseq; diagnostics + monotonicity check
    scope_hash         TEXT    NOT NULL,  -- hash of (kinds, scope) the cursor was earned under
    updated_at         TEXT    NOT NULL   -- ISO 8601 UTC
);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create plugin_event_cursors: %w", err)
	}

	slog.Info("migrated: created plugin_event_cursors table")
	return nil
}

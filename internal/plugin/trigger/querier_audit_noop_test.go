package trigger_test

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// trigger.Querier includes InsertPluginAuditEvent (the dispatcher audits
// unattested v2 event kinds). Tests that do not care about audit rows get a
// no-op; dispatcher_v2_test.go's auditRecordingQuerier records them. The
// supervisor tests' queriers get theirs in
// querier_audit_noop_supervisor_test.go, which is v1-only like they are.

func (q *fakeQuerier) InsertPluginAuditEvent(context.Context, db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return db.PluginAuditEvent{}, nil
}

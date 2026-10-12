package events_test

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// trigger.Querier includes InsertPluginAuditEvent (the dispatcher audits
// unattested v2 event kinds); the pipeline test's dispatcher fake does not
// care about audit rows.
func (q *fakeDispatcherQuerier) InsertPluginAuditEvent(context.Context, db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return db.PluginAuditEvent{}, nil
}

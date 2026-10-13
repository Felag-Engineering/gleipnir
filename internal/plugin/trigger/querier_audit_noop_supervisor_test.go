//go:build !substratev2

package trigger_test

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/db"
)

func (q *supQuerier) InsertPluginAuditEvent(context.Context, db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return db.PluginAuditEvent{}, nil
}

func (q *seqQuerier) InsertPluginAuditEvent(context.Context, db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return db.PluginAuditEvent{}, nil
}

func (q *scopeQuerier) InsertPluginAuditEvent(context.Context, db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return db.PluginAuditEvent{}, nil
}

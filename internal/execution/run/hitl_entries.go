// Package run — this file builds hitl.Router's input from the audience
// tables: the ordered candidate list a Route call walks.
package run

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/audience"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
)

// AudienceEntriesResolver resolves an audience ID into the ordered list of
// candidate entries hitl.Router walks. *DBAudienceEntriesResolver is the
// production implementation; tests substitute a fixed slice. #995 (tool-
// initiated HITL routed through audiences) shares this interface.
type AudienceEntriesResolver interface {
	ResolveEntries(ctx context.Context, audienceID string) ([]hitl.Entry, error)
}

// AudienceEntriesStore is the narrow DB surface DBAudienceEntriesResolver
// needs. *db.Queries satisfies it.
type AudienceEntriesStore interface {
	GetPluginAudienceWithEntries(ctx context.Context, audienceID string) ([]db.GetPluginAudienceWithEntriesRow, error)
	GetMCPServerByPluginInstance(ctx context.Context, pluginInstanceID *string) (db.McpServer, error)
}

// DBAudienceEntriesResolver builds hitl.Entry values from the audience
// tables, resolving each plugin-backed entry's mcp_servers row so it carries
// the ServerID hitl.Router needs to persist a pollable task.
type DBAudienceEntriesResolver struct {
	store AudienceEntriesStore
}

// NewDBAudienceEntriesResolver constructs a DBAudienceEntriesResolver over
// store (typically the run's *db.Queries).
func NewDBAudienceEntriesResolver(store AudienceEntriesStore) *DBAudienceEntriesResolver {
	return &DBAudienceEntriesResolver{store: store}
}

// ResolveEntries loads audienceID's effective entries (audience.Resolve,
// which appends the synthetic gleipnir.in-app entry unless the audience opted
// out) and fills in each plugin-backed entry's Target and ServerID.
//
// An entry the host cannot route to — no delivery target in its config, or no
// mcp_servers row for its instance — is still returned rather than dropped:
// hitl.Router records an unroutable entry as a typed skip (SkipNoTarget /
// SkipTaskNotPersisted) and tries the next one, so failing the whole audience
// here over one bad entry would throw away every OTHER entry's chance to
// answer.
func (r *DBAudienceEntriesResolver) ResolveEntries(ctx context.Context, audienceID string) ([]hitl.Entry, error) {
	rows, err := r.store.GetPluginAudienceWithEntries(ctx, audienceID)
	if err != nil {
		return nil, fmt.Errorf("load audience %s: %w", audienceID, err)
	}
	effective, err := audience.Resolve(rows)
	if err != nil {
		return nil, fmt.Errorf("resolve audience %s: %w", audienceID, err)
	}

	entries := make([]hitl.Entry, 0, len(effective))
	for _, e := range effective {
		entry := hitl.Entry{
			EntryID:    e.EntryID,
			InstanceID: e.PluginInstanceID,
			InApp:      e.Auto,
			Request:    e.Request,
		}
		if target, ok := hitl.TargetFromConfig(e.ConfigJSON); ok {
			entry.Target = target
		}
		if !e.Auto {
			serverID, err := r.serverIDFor(ctx, e.PluginInstanceID)
			if err != nil {
				return nil, fmt.Errorf("resolve mcp server for instance %s: %w", e.PluginInstanceID, err)
			}
			entry.ServerID = serverID
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// serverIDFor looks up the mcp_servers row backing instanceID. A missing row
// (no server registered for this instance yet) is not an error — it leaves
// ServerID empty, and hitl.Router's persistTask turns that into an ordinary
// skip rather than this resolver failing the whole audience over one
// instance with no endpoint attached. Any OTHER lookup failure is a real
// error: swallowing it would silently turn "the database is unreachable"
// into "this entry has no server", which is not the same fact.
func (r *DBAudienceEntriesResolver) serverIDFor(ctx context.Context, instanceID string) (string, error) {
	srv, err := r.store.GetMCPServerByPluginInstance(ctx, &instanceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return srv.ID, nil
}

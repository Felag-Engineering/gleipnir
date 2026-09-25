package run_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
)

func ptrI64(v int64) *int64   { return &v }
func ptrStr(v string) *string { return &v }

// fakeAudienceEntriesStore serves canned audience rows and server lookups
// without a database — audience.Resolve is a pure function over rows, and
// DBAudienceEntriesResolver's own job is thin enough to test the same way.
type fakeAudienceEntriesStore struct {
	rows      []db.GetPluginAudienceWithEntriesRow
	rowsErr   error
	servers   map[string]db.McpServer // keyed by plugin instance ID
	serverErr error
}

func (f *fakeAudienceEntriesStore) GetPluginAudienceWithEntries(_ context.Context, _ string) ([]db.GetPluginAudienceWithEntriesRow, error) {
	if f.rowsErr != nil {
		return nil, f.rowsErr
	}
	return f.rows, nil
}

func (f *fakeAudienceEntriesStore) GetMCPServerByPluginInstance(_ context.Context, pluginInstanceID *string) (db.McpServer, error) {
	if f.serverErr != nil {
		return db.McpServer{}, f.serverErr
	}
	srv, ok := f.servers[*pluginInstanceID]
	if !ok {
		return db.McpServer{}, sql.ErrNoRows
	}
	return srv, nil
}

func baseAudienceRow(disableInAppFallback int64) db.GetPluginAudienceWithEntriesRow {
	return db.GetPluginAudienceWithEntriesRow{
		AudienceID:           "aud-1",
		AudienceName:         "test",
		AudienceCreatedAt:    "2024-01-01T00:00:00Z",
		AudienceUpdatedAt:    "2024-01-01T00:00:00Z",
		DisableInAppFallback: disableInAppFallback,
	}
}

func pluginAudienceRow(entryID, instanceID, configJSON string, position int64) db.GetPluginAudienceWithEntriesRow {
	row := baseAudienceRow(0)
	row.EntryID = ptrStr(entryID)
	row.PluginInstanceID = ptrStr(instanceID)
	row.Position = ptrI64(position)
	row.Notify = ptrI64(0)
	row.Request = ptrI64(1)
	row.ConfigJson = ptrStr(configJSON)
	return row
}

// A plugin entry with a registered mcp_servers row resolves ServerID and the
// delivery target from its config, and the auto-appended in-app entry is
// returned unmodified (no server lookup attempted for it).
func TestDBAudienceEntriesResolver_ResolvesServerIDAndTarget(t *testing.T) {
	store := &fakeAudienceEntriesStore{
		rows: []db.GetPluginAudienceWithEntriesRow{
			pluginAudienceRow("entry-1", "inst-1", `{"delivery":"direct","address":"person-7"}`, 0),
		},
		servers: map[string]db.McpServer{
			"inst-1": {ID: "srv-1"},
		},
	}
	resolver := run.NewDBAudienceEntriesResolver(store)

	entries, err := resolver.ResolveEntries(context.Background(), "aud-1")
	if err != nil {
		t.Fatalf("ResolveEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (plugin entry + synthetic in-app)", len(entries))
	}

	plugin := entries[0]
	if plugin.EntryID != "entry-1" || plugin.InstanceID != "inst-1" {
		t.Errorf("plugin entry = %+v", plugin)
	}
	if plugin.ServerID != "srv-1" {
		t.Errorf("ServerID = %q, want srv-1", plugin.ServerID)
	}
	if plugin.Target.Address != "person-7" || plugin.Target.Delivery != mcp.ChannelDeliveryDirect {
		t.Errorf("Target = %+v", plugin.Target)
	}
	if plugin.InApp {
		t.Error("the plugin entry must not be marked InApp")
	}

	inApp := entries[1]
	if !inApp.InApp {
		t.Error("the synthetic entry must be marked InApp")
	}
	if inApp.ServerID != "" {
		t.Errorf("synthetic entry ServerID = %q, want empty (no server lookup for in-app)", inApp.ServerID)
	}
}

// A plugin instance with no registered mcp_servers row (sql.ErrNoRows) leaves
// ServerID empty rather than failing the whole audience — hitl.Router turns
// that into an ordinary skip.
func TestDBAudienceEntriesResolver_MissingServerRowLeavesServerIDEmpty(t *testing.T) {
	store := &fakeAudienceEntriesStore{
		rows: []db.GetPluginAudienceWithEntriesRow{
			pluginAudienceRow("entry-1", "inst-1", `{"delivery":"direct","address":"person-7"}`, 0),
		},
		servers: map[string]db.McpServer{},
	}
	resolver := run.NewDBAudienceEntriesResolver(store)

	entries, err := resolver.ResolveEntries(context.Background(), "aud-1")
	if err != nil {
		t.Fatalf("ResolveEntries: %v", err)
	}
	if entries[0].ServerID != "" {
		t.Errorf("ServerID = %q, want empty", entries[0].ServerID)
	}
}

// A real database failure resolving the server row must propagate — treating
// it the same as "no server" would misreport an infrastructure fault as a
// configuration one.
func TestDBAudienceEntriesResolver_ServerLookupErrorPropagates(t *testing.T) {
	store := &fakeAudienceEntriesStore{
		rows: []db.GetPluginAudienceWithEntriesRow{
			pluginAudienceRow("entry-1", "inst-1", `{"delivery":"direct","address":"person-7"}`, 0),
		},
		serverErr: errors.New("database is unreachable"),
	}
	resolver := run.NewDBAudienceEntriesResolver(store)

	if _, err := resolver.ResolveEntries(context.Background(), "aud-1"); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// A load failure on the audience rows themselves propagates too.
func TestDBAudienceEntriesResolver_LoadErrorPropagates(t *testing.T) {
	store := &fakeAudienceEntriesStore{rowsErr: errors.New("database is unreachable")}
	resolver := run.NewDBAudienceEntriesResolver(store)

	if _, err := resolver.ResolveEntries(context.Background(), "aud-1"); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// An audience with disable_in_app_fallback set carries no synthetic entry.
func TestDBAudienceEntriesResolver_DisabledFallbackHasNoInAppEntry(t *testing.T) {
	store := &fakeAudienceEntriesStore{
		rows: []db.GetPluginAudienceWithEntriesRow{
			pluginAudienceRow("entry-1", "inst-1", `{"delivery":"direct","address":"person-7"}`, 0),
		},
	}
	store.rows[0].DisableInAppFallback = 1
	store.servers = map[string]db.McpServer{"inst-1": {ID: "srv-1"}}
	resolver := run.NewDBAudienceEntriesResolver(store)

	entries, err := resolver.ResolveEntries(context.Background(), "aud-1")
	if err != nil {
		t.Fatalf("ResolveEntries: %v", err)
	}
	for _, e := range entries {
		if e.InApp {
			t.Fatal("disable_in_app_fallback=1 must not produce a synthetic entry")
		}
	}
}

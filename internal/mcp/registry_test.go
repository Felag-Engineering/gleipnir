package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
)

// newTestRegistry opens a fresh in-memory-backed SQLite store, applies the
// schema, and returns a Registry backed by it along with the store for raw
// verification queries.
func newTestRegistry(t *testing.T) (*Registry, *db.Store) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	return NewRegistry(store.Queries()), store
}

// makeMCPServer starts an httptest.Server that returns a tools/list JSON-RPC
// response containing the provided tools. Each tool map must have at minimum
// "name", "description", and "inputSchema" keys.
func makeMCPServer(t *testing.T, tools []map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"tools": tools,
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRegisterServer_HappyPath(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	tools := []map[string]any{
		{"name": "tool-a", "description": "first tool", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-b", "description": "second tool", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "test-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	// Verify server row exists.
	var serverID string
	err := rawDB.QueryRow(`SELECT id FROM mcp_servers WHERE name = 'test-server'`).Scan(&serverID)
	if err != nil {
		t.Fatalf("query server: %v", err)
	}

	// Verify exactly 2 tool rows.
	rows, err := rawDB.QueryContext(context.Background(),
		`SELECT name FROM mcp_tools WHERE server_id = ? ORDER BY name`, serverID)
	if err != nil {
		t.Fatalf("query tools: %v", err)
	}
	defer rows.Close()

	var gotNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan tool row: %v", err)
		}
		gotNames = append(gotNames, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}

	if len(gotNames) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(gotNames))
	}
	if gotNames[0] != "tool-a" {
		t.Errorf("tools[0].name = %q, want %q", gotNames[0], "tool-a")
	}
	if gotNames[1] != "tool-b" {
		t.Errorf("tools[1].name = %q, want %q", gotNames[1], "tool-b")
	}

	// last_discovered_at must be NULL after RegisterServer — only RefreshTools sets it.
	var lastDiscovered *string
	if err := rawDB.QueryRow(`SELECT last_discovered_at FROM mcp_servers WHERE id = ?`, serverID).Scan(&lastDiscovered); err != nil {
		t.Fatalf("query last_discovered_at: %v", err)
	}
	if lastDiscovered != nil {
		t.Errorf("last_discovered_at = %q, want NULL after RegisterServer", *lastDiscovered)
	}
}

func TestRegisterServer_MCPServerUnreachable(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	// Start and immediately close a server so the URL is valid but unreachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	err := reg.RegisterServer(context.Background(), "unreachable-server", url)
	if err == nil {
		t.Fatal("expected error for unreachable MCP server, got nil")
	}

	// Server row was inserted before the DiscoverTools call fails; however,
	// per spec the test asserts 0 tool rows, which is what we verify here.
	var toolCount int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_tools`).Scan(&toolCount); err != nil {
		t.Fatalf("count tool rows: %v", err)
	}
	if toolCount != 0 {
		t.Errorf("tool rows = %d, want 0 after unreachable server", toolCount)
	}
}

func TestRefreshTools_NoChanges(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	tools := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "test-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	var serverID string
	if err := rawDB.QueryRow(`SELECT id FROM mcp_servers WHERE name = 'test-server'`).Scan(&serverID); err != nil {
		t.Fatalf("query server id: %v", err)
	}

	diff, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	if len(diff.Added) != 0 {
		t.Errorf("Added = %v, want empty", diff.Added)
	}
	if len(diff.Removed) != 0 {
		t.Errorf("Removed = %v, want empty", diff.Removed)
	}
	if len(diff.Modified) != 0 {
		t.Errorf("Modified = %v, want empty", diff.Modified)
	}

	// last_discovered_at must be set after RefreshTools.
	var lastDiscovered *string
	if err := rawDB.QueryRow(`SELECT last_discovered_at FROM mcp_servers WHERE id = ?`, serverID).Scan(&lastDiscovered); err != nil {
		t.Fatalf("query last_discovered_at: %v", err)
	}
	if lastDiscovered == nil {
		t.Error("last_discovered_at is NULL after RefreshTools, want non-nil")
	}

	// No changes: has_drift must be 0.
	var hasDrift int64
	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift: %v", err)
	}
	if hasDrift != 0 {
		t.Errorf("has_drift = %d, want 0 after no-change refresh", hasDrift)
	}
}

func TestRefreshTools_AddedTools(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	oneTool := []map[string]any{
		{"name": "tool-a", "description": "desc a", "inputSchema": map[string]any{"type": "object"}},
	}
	firstSrv := makeMCPServer(t, oneTool)

	if err := reg.RegisterServer(context.Background(), "test-server", firstSrv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	var serverID string
	if err := rawDB.QueryRow(`SELECT id FROM mcp_servers WHERE name = 'test-server'`).Scan(&serverID); err != nil {
		t.Fatalf("query server id: %v", err)
	}

	// Now point the server at a handler that returns two tools.
	twoTools := []map[string]any{
		{"name": "tool-a", "description": "desc a", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-b", "description": "desc b", "inputSchema": map[string]any{"type": "object"}},
	}
	secondSrv := makeMCPServer(t, twoTools)

	// Update the server URL in the DB to point to the new handler.
	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, secondSrv.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	diff, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	if len(diff.Added) != 1 || diff.Added[0] != "tool-b" {
		t.Errorf("Added = %v, want [tool-b]", diff.Added)
	}
	if len(diff.Removed) != 0 {
		t.Errorf("Removed = %v, want empty", diff.Removed)
	}

	// Added tools: has_drift must be 1.
	var hasDrift int64
	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift: %v", err)
	}
	if hasDrift != 1 {
		t.Errorf("has_drift = %d, want 1 after added-tools refresh", hasDrift)
	}
}

func TestRefreshTools_RemovedTools(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	twoTools := []map[string]any{
		{"name": "tool-a", "description": "desc a", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-b", "description": "desc b", "inputSchema": map[string]any{"type": "object"}},
	}
	firstSrv := makeMCPServer(t, twoTools)

	if err := reg.RegisterServer(context.Background(), "test-server", firstSrv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	var serverID string
	if err := rawDB.QueryRow(`SELECT id FROM mcp_servers WHERE name = 'test-server'`).Scan(&serverID); err != nil {
		t.Fatalf("query server id: %v", err)
	}

	// Second discovery returns only tool-a.
	oneTool := []map[string]any{
		{"name": "tool-a", "description": "desc a", "inputSchema": map[string]any{"type": "object"}},
	}
	secondSrv := makeMCPServer(t, oneTool)

	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, secondSrv.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	diff, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	if len(diff.Removed) != 1 || diff.Removed[0] != "tool-b" {
		t.Errorf("Removed = %v, want [tool-b]", diff.Removed)
	}
	if len(diff.Added) != 0 {
		t.Errorf("Added = %v, want empty", diff.Added)
	}

	// Verify only 1 DB row remains.
	var count int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_tools WHERE server_id = ?`, serverID).Scan(&count); err != nil {
		t.Fatalf("count tool rows: %v", err)
	}
	if count != 1 {
		t.Errorf("tool row count = %d, want 1", count)
	}

	// Removed tools: has_drift must be 1.
	var hasDrift int64
	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift: %v", err)
	}
	if hasDrift != 1 {
		t.Errorf("has_drift = %d, want 1 after removed-tools refresh", hasDrift)
	}
}

func TestRefreshTools_ModifiedTools(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	original := []map[string]any{
		{"name": "tool-a", "description": "original desc", "inputSchema": map[string]any{"type": "object"}},
	}
	firstSrv := makeMCPServer(t, original)

	if err := reg.RegisterServer(context.Background(), "test-server", firstSrv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	var serverID string
	if err := rawDB.QueryRow(`SELECT id FROM mcp_servers WHERE name = 'test-server'`).Scan(&serverID); err != nil {
		t.Fatalf("query server id: %v", err)
	}

	// Same name, changed description.
	changed := []map[string]any{
		{"name": "tool-a", "description": "updated desc", "inputSchema": map[string]any{"type": "object"}},
	}
	secondSrv := makeMCPServer(t, changed)

	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, secondSrv.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	diff, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	if len(diff.Modified) != 1 || diff.Modified[0] != "tool-a" {
		t.Errorf("Modified = %v, want [tool-a]", diff.Modified)
	}
	if len(diff.Added) != 0 {
		t.Errorf("Added = %v, want empty", diff.Added)
	}
	if len(diff.Removed) != 0 {
		t.Errorf("Removed = %v, want empty", diff.Removed)
	}

	// Verify the DB row was updated with the new description.
	var storedDesc string
	if err := rawDB.QueryRow(`SELECT description FROM mcp_tools WHERE server_id = ? AND name = 'tool-a'`, serverID).Scan(&storedDesc); err != nil {
		t.Fatalf("query tool description: %v", err)
	}
	if storedDesc != "updated desc" {
		t.Errorf("description = %q, want %q", storedDesc, "updated desc")
	}

	// Modified tools: has_drift must be 1.
	var hasDrift int64
	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift: %v", err)
	}
	if hasDrift != 1 {
		t.Errorf("has_drift = %d, want 1 after modified-tools refresh", hasDrift)
	}
}

func TestRefreshTools_MCPServerUnreachable(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	tools := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "test-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	var serverID string
	if err := rawDB.QueryRow(`SELECT id FROM mcp_servers WHERE name = 'test-server'`).Scan(&serverID); err != nil {
		t.Fatalf("query server id: %v", err)
	}

	// Capture state before the failed refresh.
	var countBefore int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_tools WHERE server_id = ?`, serverID).Scan(&countBefore); err != nil {
		t.Fatalf("count before: %v", err)
	}

	// Point the server URL to a closed server.
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := deadSrv.URL
	deadSrv.Close()

	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, deadURL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	_, err := reg.RefreshTools(context.Background(), serverID)
	if err == nil {
		t.Fatal("expected error for unreachable MCP server, got nil")
	}

	// DB must be unchanged.
	var countAfter int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_tools WHERE server_id = ?`, serverID).Scan(&countAfter); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if countAfter != countBefore {
		t.Errorf("tool count changed from %d to %d after failed refresh", countBefore, countAfter)
	}
}

// TestResolveToolByName_HappyPath verifies that a registered tool can be
// resolved to a ready Client and bare tool name.
func TestResolveToolByName_HappyPath(t *testing.T) {
	reg, _ := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "my-tool", "description": "a tool", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	client, toolName, err := reg.ResolveToolByName(context.Background(), "my-server.my-tool")
	if err != nil {
		t.Fatalf("ResolveToolByName: %v", err)
	}
	if toolName != "my-tool" {
		t.Errorf("toolName = %q, want %q", toolName, "my-tool")
	}
	if client == nil {
		t.Error("expected a non-nil Client")
	}
}

// TestResolveToolByName_UnknownTool verifies that resolving a tool that is not
// in the registry returns an error.
func TestResolveToolByName_UnknownTool(t *testing.T) {
	reg, _ := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "real-tool", "description": "exists", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	_, _, err := reg.ResolveToolByName(context.Background(), "my-server.nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
}

// TestResolveToolByName_BadDotNotation verifies that a malformed tool name
// (no dot separator) returns an error.
func TestResolveToolByName_BadDotNotation(t *testing.T) {
	reg, _ := newTestRegistry(t)

	_, _, err := reg.ResolveToolByName(context.Background(), "nodothere")
	if err == nil {
		t.Fatal("expected error for bad dot-notation, got nil")
	}
}

// TestRefreshTools_FirstDiscoveryNoDrift verifies that when RefreshTools is
// called on a server that has no tools in the DB yet (the initial discovery
// that happens right after server creation), has_drift is NOT set even though
// all discovered tools appear in diff.Added. The first discovery establishes
// the baseline and must not be treated as drift.
func TestRefreshTools_FirstDiscoveryNoDrift(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	tools := []map[string]any{
		{"name": "tool-a", "description": "desc a", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-b", "description": "desc b", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	// Insert the server row directly — zero tools in DB — mimicking what the
	// Create handler does before it calls RefreshTools for auto-discovery.
	now := "2024-01-01T00:00:00Z"
	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-first', 'test-server', ?, ?) RETURNING id`,
		srv.URL, now,
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	diff, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	// The diff accurately reports both tools as added.
	if len(diff.Added) != 2 {
		t.Errorf("diff.Added = %v, want [tool-a tool-b]", diff.Added)
	}
	if len(diff.Removed) != 0 {
		t.Errorf("diff.Removed = %v, want empty", diff.Removed)
	}

	// First discovery must NOT set has_drift.
	var hasDrift int64
	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift: %v", err)
	}
	if hasDrift != 0 {
		t.Errorf("has_drift = %d, want 0 on first discovery", hasDrift)
	}

	// last_discovered_at must be set.
	var lastDiscovered *string
	if err := rawDB.QueryRow(`SELECT last_discovered_at FROM mcp_servers WHERE id = ?`, serverID).Scan(&lastDiscovered); err != nil {
		t.Fatalf("query last_discovered_at: %v", err)
	}
	if lastDiscovered == nil {
		t.Error("last_discovered_at is NULL after RefreshTools, want non-nil")
	}

	// Both tools must exist in the DB.
	var count int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_tools WHERE server_id = ?`, serverID).Scan(&count); err != nil {
		t.Fatalf("count tools: %v", err)
	}
	if count != 2 {
		t.Errorf("tool count = %d, want 2", count)
	}
}

// TestRefreshTools_SecondDiscoveryAfterEmptyFirst verifies that once a first
// discovery has established a non-empty baseline, a subsequent discovery with
// changed tools correctly sets has_drift=1.
func TestRefreshTools_SecondDiscoveryAfterEmptyFirst(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	// First mock returns only tool-a.
	firstTools := []map[string]any{
		{"name": "tool-a", "description": "desc a", "inputSchema": map[string]any{"type": "object"}},
	}
	firstSrv := makeMCPServer(t, firstTools)

	// Insert server row with zero tools in DB.
	now := "2024-01-01T00:00:00Z"
	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-second', 'test-server', ?, ?) RETURNING id`,
		firstSrv.URL, now,
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	// First discovery: tool-a is new baseline, must not set has_drift.
	diff, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools (first): %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "tool-a" {
		t.Errorf("first diff.Added = %v, want [tool-a]", diff.Added)
	}

	var hasDrift int64
	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift after first refresh: %v", err)
	}
	if hasDrift != 0 {
		t.Errorf("has_drift = %d after first discovery, want 0", hasDrift)
	}

	// Second mock returns tool-a + tool-b: a change relative to the baseline.
	secondTools := []map[string]any{
		{"name": "tool-a", "description": "desc a", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-b", "description": "desc b", "inputSchema": map[string]any{"type": "object"}},
	}
	secondSrv := makeMCPServer(t, secondTools)

	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, secondSrv.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	// Second discovery: tool-b is new, must set has_drift=1.
	diff, err = reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools (second): %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "tool-b" {
		t.Errorf("second diff.Added = %v, want [tool-b]", diff.Added)
	}

	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift after second refresh: %v", err)
	}
	if hasDrift != 1 {
		t.Errorf("has_drift = %d after second discovery, want 1", hasDrift)
	}
}

// TestRefreshTools_DriftClearedOnCleanRefresh verifies the full drift lifecycle:
// a discovery with changes sets has_drift=1, and a subsequent discovery that
// finds no changes clears it back to has_drift=0.
func TestRefreshTools_DriftClearedOnCleanRefresh(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	// Register server with tool-a only.
	oneTool := []map[string]any{
		{"name": "tool-a", "description": "desc a", "inputSchema": map[string]any{"type": "object"}},
	}
	firstSrv := makeMCPServer(t, oneTool)

	if err := reg.RegisterServer(context.Background(), "test-server", firstSrv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	var serverID string
	if err := rawDB.QueryRow(`SELECT id FROM mcp_servers WHERE name = 'test-server'`).Scan(&serverID); err != nil {
		t.Fatalf("query server id: %v", err)
	}

	// Point to a server that returns tool-a + tool-b: diff is non-empty, so has_drift=1.
	twoTools := []map[string]any{
		{"name": "tool-a", "description": "desc a", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-b", "description": "desc b", "inputSchema": map[string]any{"type": "object"}},
	}
	secondSrv := makeMCPServer(t, twoTools)

	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, secondSrv.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	if _, err := reg.RefreshTools(context.Background(), serverID); err != nil {
		t.Fatalf("RefreshTools (drift): %v", err)
	}

	var hasDrift int64
	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift: %v", err)
	}
	if hasDrift != 1 {
		t.Errorf("has_drift = %d, want 1 after adding tool-b", hasDrift)
	}

	// Re-discover with the same two tools — diff is empty, so has_drift must clear to 0.
	if _, err := reg.RefreshTools(context.Background(), serverID); err != nil {
		t.Fatalf("RefreshTools (clean): %v", err)
	}

	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift after clean refresh: %v", err)
	}
	if hasDrift != 0 {
		t.Errorf("has_drift = %d, want 0 after clean re-discovery", hasDrift)
	}
}

// newTestRegistryWithKey returns a Registry wired with an encryption key,
// for testing auth header decryption.
func newTestRegistryWithKey(t *testing.T, encKey []byte) (*Registry, *db.Store) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	return NewRegistry(store.Queries(), WithEncryptionKey(encKey)), store
}

// mustEncryptHeaders encrypts the given headers using the provided key and
// returns the ciphertext as a string pointer.
func mustEncryptHeaders(t *testing.T, key []byte, headers []AuthHeader) *string {
	t.Helper()
	raw, err := MarshalAuthHeaders(headers)
	if err != nil {
		t.Fatalf("marshal auth headers: %v", err)
	}
	if raw == nil {
		return nil
	}
	ciphertext, err := admin.Encrypt(key, string(raw))
	if err != nil {
		t.Fatalf("encrypt auth headers: %v", err)
	}
	return &ciphertext
}

// TestRefreshTools_SendsAuthHeaders verifies that when a server has
// auth_headers_encrypted set and the registry has an encryption key,
// the discovered headers are sent on the tools/list request.
func TestRefreshTools_SendsAuthHeaders(t *testing.T) {
	testKey := mustTestKey(t)
	reg, store := newTestRegistryWithKey(t, testKey)
	rawDB := store.DB()

	// Track requests to verify the auth header arrives.
	var capturedHeader atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		default:
			capturedHeader.Store(r.Header.Get("X-Api-Key"))
			writeJSON(w, map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{"tools": []map[string]any{
					{"name": "t", "description": "d", "inputSchema": map[string]any{"type": "object"}},
				}},
			})
		}
	}))
	t.Cleanup(srv.Close)

	// Insert a server row with encrypted auth headers.
	headers := []AuthHeader{{Name: "X-Api-Key", Value: "my-secret"}}
	ciphertext := mustEncryptHeaders(t, testKey, headers)

	now := "2024-01-01T00:00:00Z"
	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at, auth_headers_encrypted) VALUES ('srv-auth', 'auth-server', ?, ?, ?) RETURNING id`,
		srv.URL, now, *ciphertext,
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	if _, err := reg.RefreshTools(context.Background(), serverID); err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	if got, _ := capturedHeader.Load().(string); got != "my-secret" {
		t.Errorf("X-Api-Key on tools/list = %q, want %q", got, "my-secret")
	}
}

// TestRegistry_WarnAndFailOpenWhenNoEncKey verifies that when the registry has
// no encryption key but a server has stored auth headers, the client is built
// with no auth headers and no panic occurs.
func TestRegistry_WarnAndFailOpenWhenNoEncKey(t *testing.T) {
	// Registry with NO encryption key.
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	// Pick any test key just to produce a ciphertext we can store.
	anyKey := mustTestKey(t)
	headers := []AuthHeader{{Name: "X-Api-Key", Value: "secret"}}
	ciphertext := mustEncryptHeaders(t, anyKey, headers)

	now := "2024-01-01T00:00:00Z"
	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at, auth_headers_encrypted) VALUES ('srv-nokey', 'nokey-server', 'http://127.0.0.1:1', ?, ?) RETURNING id`,
		now, *ciphertext,
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	srv, err := store.Queries().GetMCPServer(context.Background(), serverID)
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}

	// newClientForServer must not panic; it logs a warning and returns a usable client.
	cl := reg.newClientForServer(srv)
	if cl == nil {
		t.Fatal("newClientForServer returned nil")
	}
	// The client should have no auth headers.
	if len(cl.authHeaders) != 0 {
		t.Errorf("authHeaders = %v, want empty (no enc key)", cl.authHeaders)
	}
}

// mustTestKey generates a deterministic 32-byte test key.
func mustTestKey(t *testing.T) []byte {
	t.Helper()
	k, err := admin.ParseEncryptionKey("aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd")
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}
	return k
}

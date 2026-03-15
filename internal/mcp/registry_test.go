package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rapp992/gleipnir/internal/db"
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

	// Verify exactly 2 tool rows with capability_role='sensor'.
	rows, err := rawDB.QueryContext(context.Background(),
		`SELECT name, capability_role FROM mcp_tools WHERE server_id = ? ORDER BY name`, serverID)
	if err != nil {
		t.Fatalf("query tools: %v", err)
	}
	defer rows.Close()

	type toolRow struct{ name, role string }
	var got []toolRow
	for rows.Next() {
		var tr toolRow
		if err := rows.Scan(&tr.name, &tr.role); err != nil {
			t.Fatalf("scan tool row: %v", err)
		}
		got = append(got, tr)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(got))
	}
	for _, tr := range got {
		if tr.role != "sensor" {
			t.Errorf("tool %q: capability_role = %q, want %q", tr.name, tr.role, "sensor")
		}
	}
	if got[0].name != "tool-a" {
		t.Errorf("tools[0].name = %q, want %q", got[0].name, "tool-a")
	}
	if got[1].name != "tool-b" {
		t.Errorf("tools[1].name = %q, want %q", got[1].name, "tool-b")
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

func TestRefreshTools_CapabilityRolePreserved(t *testing.T) {
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

	// Simulate operator overriding the capability_role to 'actuator'.
	if _, err := rawDB.Exec(
		`UPDATE mcp_tools SET capability_role = 'actuator' WHERE server_id = ? AND name = 'tool-a'`,
		serverID,
	); err != nil {
		t.Fatalf("update capability_role: %v", err)
	}

	// Refresh with the same tools — the role must survive.
	diff, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	if len(diff.Added) != 0 || len(diff.Removed) != 0 || len(diff.Modified) != 0 {
		t.Errorf("expected empty diff, got %+v", diff)
	}

	var role string
	if err := rawDB.QueryRow(
		`SELECT capability_role FROM mcp_tools WHERE server_id = ? AND name = 'tool-a'`, serverID,
	).Scan(&role); err != nil {
		t.Fatalf("query role: %v", err)
	}
	if role != "actuator" {
		t.Errorf("capability_role = %q, want %q (operator override should be preserved)", role, "actuator")
	}
}

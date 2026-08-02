package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/schemanorm"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// captureLogger replaces slog.Default() with a handler that writes JSON to
// buf for the duration of the test and restores the original default on
// cleanup. Mirrors internal/http/api/logging_test.go's helper of the same
// name; package mcp's tests never use t.Parallel() (see CLAUDE.md's
// package-level-clock convention, which applies equally to a mutated
// package-level slog default), so this is safe.
func captureLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// decodeLogLines parses newline-delimited JSON from buf into a slice of
// maps, one per log line, so tests can index fields by name.
func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	dec := json.NewDecoder(strings.NewReader(buf.String()))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		lines = append(lines, m)
	}
	return lines
}

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
//
// New protocol-era tests use NewFakeMCPServer (fakeserver.go) instead — this
// helper answers every method with a tools/list result and has no
// server/discover surface at all.
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

// makeRawMCPServer starts an httptest.Server that answers every request with
// body, written verbatim. makeMCPServer marshals map[string]any, and
// encoding/json always sorts map keys alphabetically before writing, so it
// cannot produce a duplicate object key or a controlled, non-alphabetical
// member order. This helper can, because it never round-trips through
// encoding/json on the way out.
func makeRawMCPServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write raw mcp response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRegisterServerForTest_HappyPath(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	tools := []map[string]any{
		{"name": "tool-a", "description": "first tool", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-b", "description": "second tool", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", srv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
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

	// last_discovered_at must be SET after discovery via RefreshTools.
	var lastDiscovered *string
	if err := rawDB.QueryRow(`SELECT last_discovered_at FROM mcp_servers WHERE id = ?`, serverID).Scan(&lastDiscovered); err != nil {
		t.Fatalf("query last_discovered_at: %v", err)
	}
	if lastDiscovered == nil {
		t.Error("last_discovered_at must be SET after discovery via RefreshTools.")
	}
}

func TestRegisterServerForTest_MCPServerUnreachable(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	// Start and immediately close a server so the URL is valid but unreachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "unreachable-server", url)
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

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", srv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
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

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", firstSrv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
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

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", firstSrv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
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

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", firstSrv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
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

// TestRefreshTools_PersistsCanonicalSchema is the DoD round-trip test:
// discovery must persist both the raw schema, byte-identical to what the
// server sent, and its schemanorm-normalized canonical form.
func TestRefreshTools_PersistsCanonicalSchema(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	const rawSchema = `{"b":1,"a":2}`
	srv := makeRawMCPServer(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`+
		`{"name":"tool-a","description":"desc","inputSchema":`+rawSchema+`}`+
		`]}}`)

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", srv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	wantCanonical, err := schemanorm.Normalize(json.RawMessage(rawSchema))
	if err != nil {
		t.Fatalf("schemanorm.Normalize: %v", err)
	}

	var gotRaw string
	var gotCanonical sql.NullString
	if err := rawDB.QueryRow(
		`SELECT input_schema, canonical_schema FROM mcp_tools WHERE server_id = ? AND name = 'tool-a'`, serverID,
	).Scan(&gotRaw, &gotCanonical); err != nil {
		t.Fatalf("query stored schema: %v", err)
	}

	if gotRaw != rawSchema {
		t.Errorf("input_schema = %q, want byte-identical %q", gotRaw, rawSchema)
	}
	if !gotCanonical.Valid || gotCanonical.String != string(wantCanonical) {
		t.Errorf("canonical_schema = %v, want %q", gotCanonical, wantCanonical)
	}
}

// TestRefreshTools_KeyOrderOnlyChangeIsNotDrift is the DoD item: a schema
// change that reorders object members only must not flag the tool as
// Modified once canonical_schema is already populated.
func TestRefreshTools_KeyOrderOnlyChangeIsNotDrift(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	firstSrv := makeRawMCPServer(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`+
		`{"name":"tool-a","description":"desc","inputSchema":{"a":1,"b":2,"c":3}}`+
		`]}}`)

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", firstSrv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	const reorderedSchema = `{"c":3,"a":1,"b":2}`
	secondSrv := makeRawMCPServer(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`+
		`{"name":"tool-a","description":"desc","inputSchema":`+reorderedSchema+`}`+
		`]}}`)

	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, secondSrv.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	diff, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	if len(diff.Modified) != 0 {
		t.Errorf("Modified = %v, want empty for a key-order-only schema change", diff.Modified)
	}

	var hasDrift int64
	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift: %v", err)
	}
	if hasDrift != 0 {
		t.Errorf("has_drift = %d, want 0 for a key-order-only schema change", hasDrift)
	}

	wantCanonical, err := schemanorm.Normalize(json.RawMessage(reorderedSchema))
	if err != nil {
		t.Fatalf("schemanorm.Normalize: %v", err)
	}

	var gotRaw string
	var gotCanonical sql.NullString
	if err := rawDB.QueryRow(
		`SELECT input_schema, canonical_schema FROM mcp_tools WHERE server_id = ? AND name = 'tool-a'`, serverID,
	).Scan(&gotRaw, &gotCanonical); err != nil {
		t.Fatalf("query stored schema: %v", err)
	}
	if gotRaw != reorderedSchema {
		t.Errorf("input_schema = %q, want updated to the new raw bytes %q", gotRaw, reorderedSchema)
	}
	if !gotCanonical.Valid || gotCanonical.String != string(wantCanonical) {
		t.Errorf("canonical_schema = %v, want unchanged %q", gotCanonical, wantCanonical)
	}
}

// TestRefreshTools_CanonicalizationFailureStoresNull verifies the fail-open
// contract: a schema that schemanorm rejects (here, a duplicate object key)
// must not fail the refresh or drop the tool -- only canonical_schema is
// NULL, and one WARN is logged.
func TestRefreshTools_CanonicalizationFailureStoresNull(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	const dupKeySchema = `{"type":"object","type":"array"}`
	srv := makeRawMCPServer(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`+
		`{"name":"bad-tool","description":"desc","inputSchema":`+dupKeySchema+`}`+
		`]}}`)

	// Capture only around the call under test: newTestRegistry's migration
	// logging (and RefreshTools's own first-discovery protocol-pin-change
	// WARN) are unrelated noise the "exactly 1 matching line" assertion below
	// must not be confused by.
	buf := captureLogger(t)
	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", srv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	var gotRaw string
	var gotCanonical sql.NullString
	if err := rawDB.QueryRow(
		`SELECT input_schema, canonical_schema FROM mcp_tools WHERE server_id = ? AND name = 'bad-tool'`, serverID,
	).Scan(&gotRaw, &gotCanonical); err != nil {
		t.Fatalf("query stored schema: %v", err)
	}
	if gotRaw != dupKeySchema {
		t.Errorf("input_schema = %q, want the raw schema stored despite the normalization failure", gotRaw)
	}
	if gotCanonical.Valid {
		t.Errorf("canonical_schema = %q, want NULL after a normalization failure", gotCanonical.String)
	}

	const wantMsg = "mcp tool schema failed normalization; storing raw schema with NULL canonical"
	var matches []map[string]any
	for _, l := range decodeLogLines(t, buf) {
		if l["msg"] == wantMsg {
			matches = append(matches, l)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 log line with msg %q, got %d: %v", wantMsg, len(matches), matches)
	}
	if matches[0]["tool_name"] != "bad-tool" {
		t.Errorf("log line tool_name = %v, want bad-tool", matches[0]["tool_name"])
	}
}

// TestRefreshTools_DriftFallsBackToRawWhenStoredCanonicalNull documents the
// first-refresh-after-upgrade behavior: a pre-upgrade row with NULL
// canonical_schema falls back to raw comparison, so a key-order-only change
// still flags Modified once. The refresh that flags it also backfills
// canonical_schema, so a further key-order-only refresh is clean.
func TestRefreshTools_DriftFallsBackToRawWhenStoredCanonicalNull(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	firstSrv := makeRawMCPServer(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`+
		`{"name":"tool-a","description":"desc","inputSchema":{"a":1,"b":2}}`+
		`]}}`)

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", firstSrv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	// Simulate a pre-upgrade row: canonical_schema was never populated.
	if _, err := rawDB.Exec(`UPDATE mcp_tools SET canonical_schema = NULL WHERE server_id = ?`, serverID); err != nil {
		t.Fatalf("clear canonical_schema: %v", err)
	}

	reorderedSchema := `{"b":2,"a":1}`
	secondSrv := makeRawMCPServer(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`+
		`{"name":"tool-a","description":"desc","inputSchema":`+reorderedSchema+`}`+
		`]}}`)
	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, secondSrv.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	diff, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}
	if len(diff.Modified) != 1 || diff.Modified[0] != "tool-a" {
		t.Errorf("Modified = %v, want [tool-a] on the first refresh after a NULL-canonical upgrade", diff.Modified)
	}

	var gotCanonical sql.NullString
	if err := rawDB.QueryRow(
		`SELECT canonical_schema FROM mcp_tools WHERE server_id = ? AND name = 'tool-a'`, serverID,
	).Scan(&gotCanonical); err != nil {
		t.Fatalf("query canonical_schema: %v", err)
	}
	if !gotCanonical.Valid {
		t.Fatal("canonical_schema still NULL after a refresh that discovered a schema successfully")
	}

	// A further key-order-only refresh must now be clean: canonical_schema is
	// populated, so the comparison prefers it over raw bytes.
	thirdSchema := `{"a":1,"b":2}`
	thirdSrv := makeRawMCPServer(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`+
		`{"name":"tool-a","description":"desc","inputSchema":`+thirdSchema+`}`+
		`]}}`)
	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, thirdSrv.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	diff2, err := reg.RefreshTools(context.Background(), serverID)
	if err != nil {
		t.Fatalf("RefreshTools (second refresh): %v", err)
	}
	if len(diff2.Modified) != 0 {
		t.Errorf("Modified = %v, want empty once canonical_schema is populated", diff2.Modified)
	}
}

func TestRefreshTools_MCPServerUnreachable(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	tools := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", srv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
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

	_, err = reg.RefreshTools(context.Background(), serverID)
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
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "my-tool", "description": "a tool", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
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
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "real-tool", "description": "exists", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
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

	serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "test-server", firstSrv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
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
	ciphertext, err := crypto.Encrypt(key, string(raw))
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
	k, err := crypto.ParseEncryptionKey("aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd")
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}
	return k
}

// newTestRegistryWithArbiter returns a Registry wired with the given arbiter.
func newTestRegistryWithArbiter(t *testing.T, arbiter *toolregistry.Registry) (*Registry, *db.Store) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	reg := NewRegistry(store.Queries(), WithToolNamespaceArbiter(arbiter))
	return reg, store
}

// TestRefreshTools_ReservesAddedNames verifies that RefreshTools reserves newly
// discovered tool dot-names in the arbiter under the MCP source.
func TestRefreshTools_ReservesAddedNames(t *testing.T) {
	arbiter := toolregistry.New()
	reg, store := newTestRegistryWithArbiter(t, arbiter)
	rawDB := store.DB()

	tools := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-b", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	now := "2024-01-01T00:00:00Z"
	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-res', 'my-srv', ?, ?) RETURNING id`,
		srv.URL, now,
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	if _, err := reg.RefreshTools(context.Background(), serverID); err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	wantSrc := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "my-srv"}
	for _, name := range []string{"tool-a", "tool-b"} {
		dotName := toolregistry.DotName("my-srv", name)
		got, ok := arbiter.Lookup(dotName)
		if !ok {
			t.Errorf("arbiter does not have reservation for %q", dotName)
			continue
		}
		if got != wantSrc {
			t.Errorf("arbiter[%q] = %v, want %v", dotName, got, wantSrc)
		}
	}
}

// TestRefreshTools_ReleasesRemovedNames verifies that RefreshTools releases
// arbiter reservations for tools that are no longer present on the server.
func TestRefreshTools_ReleasesRemovedNames(t *testing.T) {
	arbiter := toolregistry.New()
	reg, store := newTestRegistryWithArbiter(t, arbiter)
	rawDB := store.DB()

	twoTools := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-b", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	firstSrv := makeMCPServer(t, twoTools)

	now := "2024-01-01T00:00:00Z"
	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-rel', 'my-srv', ?, ?) RETURNING id`,
		firstSrv.URL, now,
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	if _, err := reg.RefreshTools(context.Background(), serverID); err != nil {
		t.Fatalf("RefreshTools (initial): %v", err)
	}

	// Second discovery returns only tool-a; tool-b should be released.
	oneTool := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	secondSrv := makeMCPServer(t, oneTool)
	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, secondSrv.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	if _, err := reg.RefreshTools(context.Background(), serverID); err != nil {
		t.Fatalf("RefreshTools (refresh): %v", err)
	}

	// tool-a must still be reserved.
	if _, ok := arbiter.Lookup(toolregistry.DotName("my-srv", "tool-a")); !ok {
		t.Error("tool-a should still be reserved after second refresh")
	}
	// tool-b must have been released.
	if _, ok := arbiter.Lookup(toolregistry.DotName("my-srv", "tool-b")); ok {
		t.Error("tool-b should have been released after removal")
	}
}

// TestRefreshTools_ReturnsErrToolNamespaceConflict_WhenPluginOwnsName verifies
// that RefreshTools returns ErrToolNamespaceConflict when a tool's dot-name is
// already owned by a plugin source, and the DB is not mutated.
func TestRefreshTools_ReturnsErrToolNamespaceConflict_WhenPluginOwnsName(t *testing.T) {
	arbiter := toolregistry.New()
	reg, store := newTestRegistryWithArbiter(t, arbiter)
	rawDB := store.DB()

	// Pre-claim the dot-name with a plugin source.
	pluginSrc := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "my-srv"}
	if err := arbiter.Reserve(toolregistry.DotName("my-srv", "tool-a"), pluginSrc); err != nil {
		t.Fatalf("pre-reserve: %v", err)
	}

	tools := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	now := "2024-01-01T00:00:00Z"
	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-conflict', 'my-srv', ?, ?) RETURNING id`,
		srv.URL, now,
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	_, err := reg.RefreshTools(context.Background(), serverID)
	if err == nil {
		t.Fatal("expected error for namespace conflict, got nil")
	}
	if !errors.Is(err, ErrToolNamespaceConflict) {
		t.Errorf("errors.Is(err, ErrToolNamespaceConflict) = false; err = %v", err)
	}

	// The tool must NOT have been written to the DB.
	var count int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_tools WHERE server_id = ?`, serverID).Scan(&count); err != nil {
		t.Fatalf("count mcp_tools: %v", err)
	}
	if count != 0 {
		t.Errorf("mcp_tools count = %d after conflict, want 0", count)
	}
}

// TestProbeTools_NoDBWrites verifies that ProbeTools performs discovery without
// writing any rows to mcp_servers or mcp_tools.
func TestProbeTools_NoDBWrites(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	tools := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	discovered, err := reg.ProbeTools(context.Background(), "probe-server", srv.URL, nil)
	if err != nil {
		t.Fatalf("ProbeTools: %v", err)
	}
	if len(discovered) != 1 || discovered[0].Name != "tool-a" {
		t.Errorf("ProbeTools = %v, want [{tool-a}]", discovered)
	}

	wantCanonical, err := schemanorm.Normalize(discovered[0].InputSchema)
	if err != nil {
		t.Fatalf("schemanorm.Normalize: %v", err)
	}
	if string(discovered[0].CanonicalSchema) != string(wantCanonical) {
		t.Errorf("ProbeTools CanonicalSchema = %s, want %s", discovered[0].CanonicalSchema, wantCanonical)
	}

	var serverCount int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_servers`).Scan(&serverCount); err != nil {
		t.Fatalf("count mcp_servers: %v", err)
	}
	if serverCount != 0 {
		t.Errorf("mcp_servers count = %d after ProbeTools, want 0", serverCount)
	}

	var toolCount int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_tools`).Scan(&toolCount); err != nil {
		t.Fatalf("count mcp_tools: %v", err)
	}
	if toolCount != 0 {
		t.Errorf("mcp_tools count = %d after ProbeTools, want 0", toolCount)
	}
}

// strPtr is a small helper for building *string test fixtures.
func strPtr(s string) *string { return &s }

// TestNewClientForServer_ThreadsProtocolVersion verifies that
// newClientForServer pins the client's protocolVersion from the DB row's
// protocol_version column, and that NULL or empty leaves the client
// unpinned (legacy behavior).
func TestNewClientForServer_ThreadsProtocolVersion(t *testing.T) {
	tests := []struct {
		name string
		set  *string // nil = leave the column NULL
		want string
	}{
		{name: "pinned version threads through", set: strPtr("2026-07-28"), want: "2026-07-28"},
		{name: "NULL protocol_version is unpinned", set: nil, want: ""},
		{name: "empty string protocol_version is unpinned", set: strPtr(""), want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, store := newTestRegistry(t)
			ctx := context.Background()

			if _, err := store.CreateMCPServer(ctx, db.CreateMCPServerParams{
				ID:        "srv-pv",
				Name:      "pv-server",
				Url:       "http://127.0.0.1:1",
				CreatedAt: "2024-01-01T00:00:00Z",
			}); err != nil {
				t.Fatalf("CreateMCPServer: %v", err)
			}
			if tc.set != nil {
				if err := store.UpdateMCPServerProtocolVersion(ctx, db.UpdateMCPServerProtocolVersionParams{
					ProtocolVersion: tc.set,
					ID:              "srv-pv",
				}); err != nil {
					t.Fatalf("UpdateMCPServerProtocolVersion: %v", err)
				}
			}

			srv, err := store.GetMCPServer(ctx, "srv-pv")
			if err != nil {
				t.Fatalf("GetMCPServer: %v", err)
			}

			cl := reg.newClientForServer(srv)
			if cl.protocolVersion != tc.want {
				t.Errorf("cl.protocolVersion = %q, want %q", cl.protocolVersion, tc.want)
			}
		})
	}
}

// TestRefreshTools_PinsProtocolVersion verifies the DoD's two headline
// scenarios: a modern fake pins 2026-07-28, and a legacy fake pins the
// legacy version (the server's negotiated version, or the constant when the
// server didn't echo one).
func TestRefreshTools_PinsProtocolVersion(t *testing.T) {
	tests := []struct {
		name string
		opts []FakeServerOption
		want string
	}{
		{
			name: "FakeModern pins 2026-07-28",
			opts: []FakeServerOption{WithFakeMode(FakeModern)},
			want: "2026-07-28",
		},
		{
			name: "FakeLegacy pins the legacy constant",
			opts: []FakeServerOption{WithFakeMode(FakeLegacy)},
			want: "2024-11-05",
		},
		{
			name: "FakeLegacy pins the server's negotiated version",
			opts: []FakeServerOption{WithFakeMode(FakeLegacy), WithFakeLegacyNegotiatedVersion("2025-03-26")},
			want: "2025-03-26",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, store := newTestRegistry(t)
			ctx := context.Background()
			rawDB := store.DB()

			fake := NewFakeMCPServer(tc.opts...)
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			// Insert the server row directly, mirroring the existing
			// auth-header test above, so the create path isn't involved.
			var serverID string
			if err := rawDB.QueryRow(
				`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-refresh-pin', 'pin-server', ?, ?) RETURNING id`,
				srv.URL, "2024-01-01T00:00:00Z",
			).Scan(&serverID); err != nil {
				t.Fatalf("insert server: %v", err)
			}

			if _, err := reg.RefreshTools(ctx, serverID); err != nil {
				t.Fatalf("RefreshTools: %v", err)
			}

			got, err := store.GetMCPServer(ctx, serverID)
			if err != nil {
				t.Fatalf("GetMCPServer: %v", err)
			}
			if got.ProtocolVersion == nil || *got.ProtocolVersion != tc.want {
				t.Errorf("protocol_version = %v, want %q", got.ProtocolVersion, tc.want)
			}
		})
	}
}

// TestRefreshTools_ReprobesAndUpdatesPin verifies the DoD's third scenario:
// refresh re-probes and updates a stale pin, and the returned ToolDiff is
// still correct alongside the updated pin.
func TestRefreshTools_ReprobesAndUpdatesPin(t *testing.T) {
	reg, store := newTestRegistry(t)
	ctx := context.Background()
	rawDB := store.DB()

	fake := NewFakeMCPServer(WithFakeMode(FakeModern), WithFakeTools(
		Tool{Name: "tool-a", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
	))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at, protocol_version) VALUES ('srv-reprobe', 'reprobe-server', ?, ?, '2024-11-05') RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	diff, err := reg.RefreshTools(ctx, serverID)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "tool-a" {
		t.Errorf("diff.Added = %v, want [tool-a]", diff.Added)
	}

	got, err := store.GetMCPServer(ctx, serverID)
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	if got.ProtocolVersion == nil || *got.ProtocolVersion != "2026-07-28" {
		t.Errorf("protocol_version = %v, want %q (re-probed and updated)", got.ProtocolVersion, "2026-07-28")
	}
}

// TestRefreshTools_ProbeFailureIsFailOpen verifies that a protocol probe
// that cannot pin a version — an inconclusive discover status, or a
// confirmed-modern server sharing no version with us — never fails
// RefreshTools and leaves protocol_version untouched (NULL).
func TestRefreshTools_ProbeFailureIsFailOpen(t *testing.T) {
	tests := []struct {
		name string
		opts []FakeServerOption
	}{
		{
			name: "inconclusive discover status",
			opts: []FakeServerOption{WithFakeMode(FakeLegacy), WithFakeDiscoverStatus(http.StatusInternalServerError)},
		},
		{
			name: "confirmed-modern server with no mutually supported version",
			opts: []FakeServerOption{WithFakeMode(FakeVersionMismatch), WithFakeSupportedVersions("2025-11-25")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, store := newTestRegistry(t)
			ctx := context.Background()
			rawDB := store.DB()

			fake := NewFakeMCPServer(tc.opts...)
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			var serverID string
			if err := rawDB.QueryRow(
				`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-failopen', 'failopen-server', ?, ?) RETURNING id`,
				srv.URL, "2024-01-01T00:00:00Z",
			).Scan(&serverID); err != nil {
				t.Fatalf("insert server: %v", err)
			}

			diff, err := reg.RefreshTools(ctx, serverID)
			if err != nil {
				t.Fatalf("RefreshTools: expected nil error (fail-open), got %v", err)
			}
			if len(diff.Added) != 1 || diff.Added[0] != "tool-a" {
				t.Errorf("diff.Added = %v, want [tool-a] (the fake's default tool)", diff.Added)
			}

			got, err := store.GetMCPServer(ctx, serverID)
			if err != nil {
				t.Fatalf("GetMCPServer: %v", err)
			}
			if got.ProtocolVersion != nil {
				t.Errorf("protocol_version = %v, want nil (probe failure leaves the pin untouched)", *got.ProtocolVersion)
			}
		})
	}
}

// TestRefreshTools_NeverDowngradesModernPin is the Finding 2 reproducer
// (security review, #737 cycle 2): a row already pinned to a modern version,
// re-probed against a server whose server/discover POST now returns an
// ambiguous 4xx (a WAF, a hostile proxy, a flaky deploy) while the legacy
// initialize handshake still succeeds, must NOT be silently rewritten to the
// legacy version — before the fix this was a single-request, invisible,
// permanent demotion.
func TestRefreshTools_NeverDowngradesModernPin(t *testing.T) {
	reg, store := newTestRegistry(t)
	ctx := context.Background()
	rawDB := store.DB()

	// WithFakeDiscoverStatus forces an empty-bodied 400 regardless of mode —
	// exactly the "4xx with no recognized modern JSON-RPC error" shape that
	// classifies discoverLegacy — while every other mode-driven behavior
	// (including the legacy initialize handshake) is untouched.
	fake := NewFakeMCPServer(WithFakeMode(FakeModern), WithFakeDiscoverStatus(http.StatusBadRequest))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at, protocol_version) VALUES ('srv-nodowngrade', 'nodowngrade-server', ?, ?, '2026-07-28') RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	diff, err := reg.RefreshTools(ctx, serverID)
	if err != nil {
		t.Fatalf("RefreshTools: expected nil error (fail-open), got %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "tool-a" {
		t.Errorf("diff.Added = %v, want [tool-a] (tool discovery over the untouched legacy handshake is unaffected)", diff.Added)
	}

	got, err := store.GetMCPServer(ctx, serverID)
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	if got.ProtocolVersion == nil || *got.ProtocolVersion != "2026-07-28" {
		t.Errorf("protocol_version = %v, want unchanged %q — a single ambiguous 4xx must not downgrade an established modern pin",
			got.ProtocolVersion, "2026-07-28")
	}
}

// TestRefreshTools_ConcurrentRefreshesCannotRaceThePin is the regression test
// for Finding 1 (security review, #737 cycle 3): the no-downgrade guard used
// to be a non-atomic read-then-write — refreshProtocolVersion read `previous`
// from srv's in-memory snapshot and then wrote unconditionally. Two
// concurrent refreshes (e.g. an operator double-clicking Discover on a
// freshly-registered modern server) could both snapshot the same stale NULL
// `previous`, race past the in-memory guard, and whichever write landed last
// would win — even a legacy write demoting a server the OTHER goroutine had
// just proven modern. Reproduced 5/5 before the fix.
//
// The fake server answers server/discover modern on the FIRST request it
// receives and legacy (an empty 400 — a shape classifyDiscoverResponse
// resolves to discoverLegacy) on every request after that, modeling the
// finding's "the peer answers modern once and 400 once" scenario
// deterministically: exactly one of the two concurrent probes is classified
// modern and the other legacy, regardless of which goroutine's HTTP request
// actually wins the race to the server.
//
// UpdateMCPServerProtocolVersionIfNotModern moves the guard from an
// in-memory check into the UPDATE's SQL WHERE clause, evaluated against the
// row's LIVE state, so the modern pin must survive regardless of which
// goroutine's write reaches the database first.
func TestRefreshTools_ConcurrentRefreshesCannotRaceThePin(t *testing.T) {
	reg, store := newTestRegistry(t)
	ctx := context.Background()
	rawDB := store.DB()

	var discoverCalls atomic.Int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		var decoded struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &decoded) //nolint:errcheck

		switch decoded.Method {
		case "server/discover":
			if discoverCalls.Add(1) == 1 {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"jsonrpc": "2.0", "id": 1,
					"result": map[string]any{
						"resultType":        "complete",
						"supportedVersions": []string{"2026-07-28"},
					},
				})
				return
			}
			// Every subsequent probe: an unrecognized-shaped 400 with no
			// JSON-RPC envelope, which classifies discoverLegacy.
			w.WriteHeader(http.StatusBadRequest)
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "race-session")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": 1, "result": map[string]any{},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-race', 'race-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	// Both goroutines snapshot the same NULL protocol_version, exactly as two
	// concurrent RefreshTools calls would via their own independent
	// GetMCPServer read.
	srv1, err := store.GetMCPServer(ctx, serverID)
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	srv2 := srv1

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); reg.refreshProtocolVersion(ctx, &srv1) }()
	go func() { defer wg.Done(); reg.refreshProtocolVersion(ctx, &srv2) }()
	wg.Wait()

	got, err := store.GetMCPServer(ctx, serverID)
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	if got.ProtocolVersion == nil || *got.ProtocolVersion != "2026-07-28" {
		t.Errorf("protocol_version = %v, want %q — a concurrent legacy probe must never win a race against a modern one",
			got.ProtocolVersion, "2026-07-28")
	}
}

// TestRefreshTools_LogsEveryPinChangeAtWarn verifies the second half of the
// Finding 2 fix: because mcp_servers config changes have no dedicated audit
// event in this repo (see refreshProtocolVersion's doc comment), every pin
// write — and every blocked downgrade attempt — must be visible in the logs
// at WARN, not just on probe error.
func TestRefreshTools_LogsEveryPinChangeAtWarn(t *testing.T) {
	tests := []struct {
		name        string
		opts        []FakeServerOption
		initialPin  string
		wantMessage string
	}{
		{
			name:        "a genuine pin change is logged",
			opts:        []FakeServerOption{WithFakeMode(FakeModern)},
			initialPin:  "2024-11-05",
			wantMessage: "mcp protocol version pin changed",
		},
		{
			name:        "a blocked downgrade attempt is logged",
			opts:        []FakeServerOption{WithFakeMode(FakeModern), WithFakeDiscoverStatus(http.StatusBadRequest)},
			initialPin:  "2026-07-28",
			wantMessage: "mcp protocol probe would downgrade an established modern pin; keeping existing pin, explicit operator action required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, store := newTestRegistry(t)
			ctx := context.Background()
			rawDB := store.DB()

			fake := NewFakeMCPServer(tc.opts...)
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			var serverID string
			if err := rawDB.QueryRow(
				`INSERT INTO mcp_servers (id, name, url, created_at, protocol_version) VALUES ('srv-warnlog', 'warnlog-server', ?, ?, ?) RETURNING id`,
				srv.URL, "2024-01-01T00:00:00Z", tc.initialPin,
			).Scan(&serverID); err != nil {
				t.Fatalf("insert server: %v", err)
			}

			buf := captureLogger(t)
			if _, err := reg.RefreshTools(ctx, serverID); err != nil {
				t.Fatalf("RefreshTools: %v", err)
			}

			var found bool
			for _, l := range decodeLogLines(t, buf) {
				if l["level"] == "WARN" && l["msg"] == tc.wantMessage {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a WARN log with msg %q, got lines: %v", tc.wantMessage, decodeLogLines(t, buf))
			}
		})
	}
}

// TestRefreshTools_ProbeSequenceBoundedBySingleTimeout verifies Finding 5
// (security review, #737 cycle 2): the protocol probe and tool discovery
// share ONE context.WithTimeout budget rather than each of the (up to six)
// MCP round trips getting its own full GLEIPNIR_MCP_TIMEOUT allowance. A
// server that responds slowly but successfully to every individual request
// must still cause the whole RefreshTools call to fail once the shared
// budget is exhausted, rather than succeeding after the sum of per-request
// waits.
func TestRefreshTools_ProbeSequenceBoundedBySingleTimeout(t *testing.T) {
	const (
		budget = 200 * time.Millisecond // WithMCPTimeout — the PER-REQUEST allowance
		delay  = 1 * time.Second        // per slow round trip; 5 slow round trips sum to 5s if unbounded
	)
	// ProbeTimeout() applies probeTimeoutMultiplier (Finding 5, security
	// review, #737 cycle 3) so this is the actual whole-sequence budget
	// RefreshTools' context.WithTimeout is built from, not budget itself.
	sharedTimeout := probeTimeoutMultiplier * budget

	slowHandler := func(w http.ResponseWriter, r *http.Request) {
		var decoded struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &decoded) //nolint:errcheck

		switch decoded.Method {
		case "server/discover":
			// Fast, unambiguous legacy signal — no need to be slow here; the
			// slow round trips are the legacy handshake and tool discovery.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("404 page not found")) //nolint:errcheck
		case "initialize":
			time.Sleep(delay)
			w.Header().Set("Mcp-Session-Id", "slow-session")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": 1, "result": map[string]any{},
			})
		case "notifications/initialized":
			time.Sleep(delay)
			w.WriteHeader(http.StatusOK)
		case "tools/list":
			time.Sleep(delay)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{"tools": []map[string]any{
					{"name": "tool-a", "description": "d", "inputSchema": map[string]any{"type": "object"}},
				}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(slowHandler))
	t.Cleanup(srv.Close)

	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	reg := NewRegistry(store.Queries(), WithMCPTimeout(budget))

	var serverID string
	if err := store.DB().QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-slowbudget', 'slowbudget-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	start := time.Now()
	_, err = reg.RefreshTools(context.Background(), serverID)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("RefreshTools: expected an error once the shared budget is exhausted, got nil")
	}
	// Pre-fix, each of the 5 slow round trips gets its own ~budget-sized
	// per-request client.Timeout, all comfortably above delay, so the whole
	// sequence succeeds after roughly 5*delay (5s). Post-fix, one shared
	// context.WithTimeout(ctx, sharedTimeout) governs the entire sequence, so
	// it must fail near sharedTimeout, not near 5*delay.
	//
	// Code review, #737 cycle 2/3: CLAUDE.md requires a wall-clock assertion
	// that cannot be turned into a signal to use a deadline at least 5x the
	// expected duration so CI scheduling jitter cannot flake it. The prior
	// version of this test used a 1.5x ceiling (only ~150ms of slack over a
	// 300ms budget), which passed locally but is exactly the shape of flake
	// this repo has been bitten by before. 5x sharedTimeout is generous
	// enough to absorb CI scheduling jitter around context cancellation while
	// staying well under the 5*delay (5s) a regression to per-round-trip
	// timeouts would produce — the two are more than 2x apart, so the
	// assertion still genuinely proves the sequence is bounded rather than
	// just being loose enough to always pass.
	if ceiling := 5 * sharedTimeout; elapsed > ceiling {
		t.Errorf("RefreshTools took %s, want under %s (single shared timeout, not per-round-trip)", elapsed, ceiling)
	}
}

// TestResolvedTool_SchemaForHeaderParams locks the one sanctioned fallback
// from CanonicalSchema to InputSchema: canonical when present, raw
// InputSchema otherwise (nil, empty-but-non-nil, or absent canonical all
// fall back), never neither.
func TestResolvedTool_SchemaForHeaderParams(t *testing.T) {
	canonical := json.RawMessage(`{"properties":{"a":{}}}`)
	raw := json.RawMessage(`{"properties":{"a":{},"b":{}}}`)

	tests := []struct {
		name            string
		canonicalSchema json.RawMessage
		inputSchema     json.RawMessage
		want            json.RawMessage
	}{
		{
			name:            "canonical present → canonical returned",
			canonicalSchema: canonical,
			inputSchema:     raw,
			want:            canonical,
		},
		{
			name:            "canonical nil → InputSchema returned",
			canonicalSchema: nil,
			inputSchema:     raw,
			want:            raw,
		},
		{
			name:            "canonical empty-but-non-nil → InputSchema returned",
			canonicalSchema: json.RawMessage{},
			inputSchema:     raw,
			want:            raw,
		},
		{
			name:            "both empty → nil",
			canonicalSchema: nil,
			inputSchema:     nil,
			want:            nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := ResolvedTool{
				CanonicalSchema: tc.canonicalSchema,
				InputSchema:     tc.inputSchema,
			}
			got := rt.SchemaForHeaderParams()
			if string(got) != string(tc.want) {
				t.Errorf("SchemaForHeaderParams() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestResolvedTool_SchemaForNarrowing mirrors TestResolvedTool_SchemaForHeaderParams
// exactly: SchemaForNarrowing shares the same canonical-first, raw-fallback
// selection rule.
func TestResolvedTool_SchemaForNarrowing(t *testing.T) {
	canonical := json.RawMessage(`{"properties":{"a":{}}}`)
	raw := json.RawMessage(`{"properties":{"a":{},"b":{}}}`)

	tests := []struct {
		name            string
		canonicalSchema json.RawMessage
		inputSchema     json.RawMessage
		want            json.RawMessage
	}{
		{
			name:            "canonical present → canonical returned",
			canonicalSchema: canonical,
			inputSchema:     raw,
			want:            canonical,
		},
		{
			name:            "canonical nil → InputSchema returned",
			canonicalSchema: nil,
			inputSchema:     raw,
			want:            raw,
		},
		{
			name:            "canonical empty-but-non-nil → InputSchema returned",
			canonicalSchema: json.RawMessage{},
			inputSchema:     raw,
			want:            raw,
		},
		{
			name:            "both empty → nil",
			canonicalSchema: nil,
			inputSchema:     nil,
			want:            nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := ResolvedTool{
				CanonicalSchema: tc.canonicalSchema,
				InputSchema:     tc.inputSchema,
			}
			got := rt.SchemaForNarrowing()
			if string(got) != string(tc.want) {
				t.Errorf("SchemaForNarrowing() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestLookupTool_MissingTool(t *testing.T) {
	reg, _ := newTestRegistry(t)

	exists, canonical, err := reg.LookupTool(context.Background(), "no-such-server", "no-such-tool")
	if err != nil {
		t.Fatalf("LookupTool: %v", err)
	}
	if exists {
		t.Error("exists = true, want false")
	}
	if canonical != nil {
		t.Errorf("canonical = %v, want nil", canonical)
	}
}

func TestLookupTool_ReturnsCanonicalSchema(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{}}}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	registered, err := store.Queries().GetMCPToolByServerAndName(context.Background(), db.GetMCPToolByServerAndNameParams{
		ServerName: "my-server",
		ToolName:   "tool-a",
	})
	if err != nil {
		t.Fatalf("GetMCPToolByServerAndName: %v", err)
	}
	if registered.CanonicalSchema == nil {
		t.Fatal("expected the discovered tool to have a stored canonical_schema")
	}

	exists, canonical, err := reg.LookupTool(context.Background(), "my-server", "tool-a")
	if err != nil {
		t.Fatalf("LookupTool: %v", err)
	}
	if !exists {
		t.Fatal("exists = false, want true")
	}
	if string(canonical) != *registered.CanonicalSchema {
		t.Errorf("canonical = %s, want %s", canonical, *registered.CanonicalSchema)
	}
}

func TestLookupTool_NullCanonicalReturnsNil(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	tools := []map[string]any{
		{"name": "tool-null", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool-empty", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	if _, err := rawDB.Exec(`UPDATE mcp_tools SET canonical_schema = NULL WHERE name = ?`, "tool-null"); err != nil {
		t.Fatalf("null out canonical_schema: %v", err)
	}
	if _, err := rawDB.Exec(`UPDATE mcp_tools SET canonical_schema = '' WHERE name = ?`, "tool-empty"); err != nil {
		t.Fatalf("empty out canonical_schema: %v", err)
	}

	for _, toolName := range []string{"tool-null", "tool-empty"} {
		exists, canonical, err := reg.LookupTool(context.Background(), "my-server", toolName)
		if err != nil {
			t.Fatalf("LookupTool(%s): %v", toolName, err)
		}
		if !exists {
			t.Errorf("LookupTool(%s): exists = false, want true", toolName)
		}
		if canonical != nil {
			t.Errorf("LookupTool(%s): canonical = %v, want nil", toolName, canonical)
		}
	}
}

func TestLookupTool_DisabledToolStillExists(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "tool-a", "description": "desc", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	registered, err := store.Queries().GetMCPToolByServerAndName(context.Background(), db.GetMCPToolByServerAndNameParams{
		ServerName: "my-server",
		ToolName:   "tool-a",
	})
	if err != nil {
		t.Fatalf("GetMCPToolByServerAndName: %v", err)
	}
	if err := store.Queries().SetMCPToolEnabled(context.Background(), db.SetMCPToolEnabledParams{
		ID:      registered.ID,
		Enabled: 0,
	}); err != nil {
		t.Fatalf("SetMCPToolEnabled: %v", err)
	}

	exists, _, err := reg.LookupTool(context.Background(), "my-server", "tool-a")
	if err != nil {
		t.Fatalf("LookupTool: %v", err)
	}
	if !exists {
		t.Error("exists = false, want true — a disabled tool must still count as existing (enablement is a run-start gate, not a save gate)")
	}
}

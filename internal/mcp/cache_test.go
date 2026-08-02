package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// freezeClock swaps package mcp's shared timeNow to a fake clock starting at
// "at", restoring the real clock in t.Cleanup. The returned advance function
// moves the fake clock forward by d. timeNow is a package-level var, so
// every test using this helper must not call t.Parallel() (CLAUDE.md
// "Testing time-dependent code"; registry_test.go's package doc already
// records the package-wide no-t.Parallel() rule this makes binding).
func freezeClock(t *testing.T, at time.Time) (advance func(d time.Duration)) {
	t.Helper()
	now := at
	orig := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = orig })
	return func(d time.Duration) { now = now.Add(d) }
}

// TestParseCacheHint covers every absence/gating/pass-through/boundary/
// rejection/clamp case parseCacheHint's doc comment enumerates.
func TestParseCacheHint(t *testing.T) {
	tests := []struct {
		name        string
		modern      bool
		rawTTLMs    json.RawMessage
		rawScope    json.RawMessage
		wantPresent bool
		wantTTL     time.Duration
		wantScope   cacheScope
	}{
		{name: "both raws nil", modern: true, rawTTLMs: nil, rawScope: nil, wantPresent: false},
		{name: "not modern despite an otherwise valid hint", modern: false,
			rawTTLMs: json.RawMessage(`1000`), rawScope: json.RawMessage(`"private"`), wantPresent: false},
		{name: "cacheScope absent", modern: true, rawTTLMs: json.RawMessage(`1000`), rawScope: nil, wantPresent: false},
		{name: "cacheScope unrecognized value", modern: true,
			rawTTLMs: json.RawMessage(`1000`), rawScope: json.RawMessage(`"shared"`), wantPresent: false},
		{name: "cacheScope non-string", modern: true,
			rawTTLMs: json.RawMessage(`1000`), rawScope: json.RawMessage(`123`), wantPresent: false},

		{name: "1s private below the ceiling", modern: true,
			rawTTLMs: json.RawMessage(`1000`), rawScope: json.RawMessage(`"private"`),
			wantPresent: true, wantTTL: 1 * time.Second, wantScope: cacheScopePrivate},
		{name: "30s public below the ceiling", modern: true,
			rawTTLMs: json.RawMessage(`30000`), rawScope: json.RawMessage(`"public"`),
			wantPresent: true, wantTTL: 30 * time.Second, wantScope: cacheScopePublic},

		{name: "exactly at the ceiling is not altered", modern: true,
			rawTTLMs: json.RawMessage(`60000`), rawScope: json.RawMessage(`"public"`),
			wantPresent: true, wantTTL: maxCacheHintTTL, wantScope: cacheScopePublic},

		{name: "zero ttl is present but immediately stale", modern: true,
			rawTTLMs: json.RawMessage(`0`), rawScope: json.RawMessage(`"public"`),
			wantPresent: true, wantTTL: 0, wantScope: cacheScopePublic},

		{name: "negative ttl rejected", modern: true,
			rawTTLMs: json.RawMessage(`-1`), rawScope: json.RawMessage(`"public"`), wantPresent: false},
		{name: "fractional ttl rejected", modern: true,
			rawTTLMs: json.RawMessage(`1.5`), rawScope: json.RawMessage(`"public"`), wantPresent: false},
		{name: "string ttl rejected", modern: true,
			rawTTLMs: json.RawMessage(`"60000"`), rawScope: json.RawMessage(`"public"`), wantPresent: false},
		{name: "object ttl rejected", modern: true,
			rawTTLMs: json.RawMessage(`{}`), rawScope: json.RawMessage(`"public"`), wantPresent: false},
		{name: "array ttl rejected", modern: true,
			rawTTLMs: json.RawMessage(`[]`), rawScope: json.RawMessage(`"public"`), wantPresent: false},
		{name: "null ttl rejected", modern: true,
			rawTTLMs: json.RawMessage(`null`), rawScope: json.RawMessage(`"public"`), wantPresent: false},

		{name: "one hour clamps to the ceiling", modern: true,
			rawTTLMs: json.RawMessage(`3600000`), rawScope: json.RawMessage(`"private"`),
			wantPresent: true, wantTTL: maxCacheHintTTL, wantScope: cacheScopePrivate},
		{name: "MaxInt64 clamps without overflow or panic", modern: true,
			rawTTLMs: json.RawMessage(`9223372036854775807`), rawScope: json.RawMessage(`"public"`),
			wantPresent: true, wantTTL: maxCacheHintTTL, wantScope: cacheScopePublic},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCacheHint(tc.modern, tc.rawTTLMs, tc.rawScope)
			if got.Present != tc.wantPresent {
				t.Fatalf("Present = %v, want %v", got.Present, tc.wantPresent)
			}
			if !tc.wantPresent {
				return
			}
			if got.TTL != tc.wantTTL {
				t.Errorf("TTL = %v, want %v", got.TTL, tc.wantTTL)
			}
			if got.Scope != tc.wantScope {
				t.Errorf("Scope = %q, want %q", got.Scope, tc.wantScope)
			}
			if got.TTL < 0 {
				t.Errorf("TTL = %v, want a non-negative duration (no ms->ns overflow)", got.TTL)
			}
		})
	}
}

// TestClientForServer_ReusesUntilConfigChanges verifies clientForServer's
// invalidation-by-content-comparison over the four serverConfig columns: an
// unchanged row always returns the identical *Client, and a change to any
// one of the four columns forces a rebuild.
func TestClientForServer_ReusesUntilConfigChanges(t *testing.T) {
	protoA := "2026-07-28"
	authA := "ciphertext-a"

	tests := []struct {
		name   string
		mutate func(db.McpServer) db.McpServer
	}{
		{"name", func(s db.McpServer) db.McpServer { s.Name = "server-b"; return s }},
		{"url", func(s db.McpServer) db.McpServer { s.Url = "http://example.invalid/b"; return s }},
		{"protocol_version", func(s db.McpServer) db.McpServer { s.ProtocolVersion = &protoA; return s }},
		{"auth_headers_encrypted", func(s db.McpServer) db.McpServer { s.AuthHeadersEncrypted = &authA; return s }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, _ := newTestRegistry(t)
			srv := db.McpServer{ID: "srv-" + tc.name, Name: "server-a", Url: "http://example.invalid/a"}

			first := reg.clientForServer(srv)
			second := reg.clientForServer(srv)
			if first != second {
				t.Fatal("clientForServer returned a different *Client for an unchanged serverConfig")
			}

			third := reg.clientForServer(tc.mutate(srv))
			if third == first {
				t.Errorf("clientForServer after %s changed reused the cached *Client, want a rebuild", tc.name)
			}
		})
	}
}

// TestResolveToolByName_ReusesClientAcrossCalls verifies the poll engine's
// win: two resolves of the same dot-name against an unchanged server return
// the identical *Client pointer.
func TestResolveToolByName_ReusesClientAcrossCalls(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "my-tool", "description": "a tool", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	client1, _, err := reg.ResolveToolByName(context.Background(), "my-server.my-tool")
	if err != nil {
		t.Fatalf("ResolveToolByName (1st): %v", err)
	}
	client2, _, err := reg.ResolveToolByName(context.Background(), "my-server.my-tool")
	if err != nil {
		t.Fatalf("ResolveToolByName (2nd): %v", err)
	}
	if client1 != client2 {
		t.Error("ResolveToolByName built a new *Client on the second call for an unchanged server")
	}
}

// sessionCountingLegacyServer starts an httptest server that behaves as a
// minimal legacy MCP server serving a single tool named toolName. It mints
// "Mcp-Session-Id: test-session" on initialize (mandatory per the plan's
// correction (E): a session-less stub would force a reused client to
// re-initialize on every call regardless of caching), and counts initialize
// and tools/call requests atomically. server/discover answers an opaque 404
// — the legacy hallmark (classifyDiscoverResponse) — so the resolved client
// keeps legacy session shaping.
func sessionCountingLegacyServer(t *testing.T, toolName string) (srv *httptest.Server, initCount, callCount *atomic.Int64) {
	t.Helper()
	initCount = &atomic.Int64{}
	callCount = &atomic.Int64{}

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			initCount.Add(1)
			w.Header().Set("Mcp-Session-Id", "test-session")
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		case methodToolsList:
			writeJSON(w, map[string]any{
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": toolName, "description": "a tool", "inputSchema": map[string]any{"type": "object"}},
					},
				},
			})
		case methodToolsCall:
			callCount.Add(1)
			writeJSON(w, map[string]any{
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "ok"}},
					"isError": false,
				},
			})
		default:
			// server/discover and anything else this fake does not model.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("404 page not found")) //nolint:errcheck
		}
	}))
	t.Cleanup(srv.Close)
	return srv, initCount, callCount
}

// TestResolveToolByName_LegacyHandshakeHappensOnceAcrossResolves is the
// poll-engine reuse win, asserted synchronously: resolving the same tool
// twice and calling it each time must pay exactly one legacy initialize
// handshake, not one per resolve.
func TestResolveToolByName_LegacyHandshakeHappensOnceAcrossResolves(t *testing.T) {
	srv, initCount, callCount := sessionCountingLegacyServer(t, "my-tool")

	reg, store := newTestRegistry(t)
	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	// Snapshot after registration: RefreshTools's own protocol probe and
	// discovery (both via throwaway clients, never the resolve-path cache)
	// already paid for initialize handshakes of their own. Only the delta
	// from here on is under test.
	initBefore := initCount.Load()

	for i := 0; i < 2; i++ {
		client, toolName, err := reg.ResolveToolByName(context.Background(), "my-server.my-tool")
		if err != nil {
			t.Fatalf("ResolveToolByName (%d): %v", i, err)
		}
		if _, err := client.CallTool(context.Background(), toolName, nil, CallOptions{}); err != nil {
			t.Fatalf("CallTool (%d): %v", i, err)
		}
	}

	if got := initCount.Load() - initBefore; got != 1 {
		t.Errorf("initialize delta = %d, want 1 — ResolveToolByName should reuse the cached client's session", got)
	}
	if got := callCount.Load(); got != 2 {
		t.Errorf("tools/call count = %d, want 2", got)
	}
}

// TestResolveToolByName_ConfigChangeRebuildsClient verifies that a change to
// a live mcp_servers row is observed on the very next resolve: a url change
// lands on the new server (and never touches the old one again), and an
// auth-header change rebuilds the client even though the url is unchanged.
func TestResolveToolByName_ConfigChangeRebuildsClient(t *testing.T) {
	t.Run("url change lands on the new server", func(t *testing.T) {
		reg, store := newTestRegistry(t)
		rawDB := store.DB()

		srvA, _, callsA := sessionCountingLegacyServer(t, "my-tool")
		srvB, _, callsB := sessionCountingLegacyServer(t, "my-tool")

		serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srvA.URL)
		if err != nil {
			t.Fatalf("RegisterServerForTest: %v", err)
		}

		client1, toolName, err := reg.ResolveToolByName(context.Background(), "my-server.my-tool")
		if err != nil {
			t.Fatalf("ResolveToolByName (A): %v", err)
		}
		if _, err := client1.CallTool(context.Background(), toolName, nil, CallOptions{}); err != nil {
			t.Fatalf("CallTool (A): %v", err)
		}
		if got := callsA.Load(); got != 1 {
			t.Fatalf("server A tools/call count = %d, want 1", got)
		}

		if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, srvB.URL, serverID); err != nil {
			t.Fatalf("update url: %v", err)
		}

		client2, toolName, err := reg.ResolveToolByName(context.Background(), "my-server.my-tool")
		if err != nil {
			t.Fatalf("ResolveToolByName (B): %v", err)
		}
		if client2 == client1 {
			t.Error("expected a rebuilt *Client after the server's url changed")
		}
		if _, err := client2.CallTool(context.Background(), toolName, nil, CallOptions{}); err != nil {
			t.Fatalf("CallTool (B): %v", err)
		}
		if got := callsB.Load(); got != 1 {
			t.Errorf("server B tools/call count = %d, want 1", got)
		}
		if got := callsA.Load(); got != 1 {
			t.Errorf("server A tools/call count = %d, want still 1 (unchanged after the url moved away)", got)
		}
	})

	t.Run("auth header change rebuilds the client", func(t *testing.T) {
		testKey := mustTestKey(t)
		reg, store := newTestRegistryWithKey(t, testKey)
		rawDB := store.DB()

		srv, _, _ := sessionCountingLegacyServer(t, "my-tool")

		serverID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL)
		if err != nil {
			t.Fatalf("RegisterServerForTest: %v", err)
		}

		client1, _, err := reg.ResolveToolByName(context.Background(), "my-server.my-tool")
		if err != nil {
			t.Fatalf("ResolveToolByName (before): %v", err)
		}

		ciphertext := mustEncryptHeaders(t, testKey, []AuthHeader{{Name: "X-Api-Key", Value: "rotated"}})
		if _, err := rawDB.Exec(`UPDATE mcp_servers SET auth_headers_encrypted = ? WHERE id = ?`, *ciphertext, serverID); err != nil {
			t.Fatalf("update auth headers: %v", err)
		}

		client2, _, err := reg.ResolveToolByName(context.Background(), "my-server.my-tool")
		if err != nil {
			t.Fatalf("ResolveToolByName (after): %v", err)
		}
		if client2 == client1 {
			t.Error("expected a rebuilt *Client after auth_headers_encrypted changed")
		}
	})
}

// TestRefreshTools_ServesToolsListFromCacheWithinTTL is the DoD test: a
// modern server's advertised ttlMs is honored verbatim (30s, deliberately
// below maxCacheHintTTL so this test exercises the honored-verbatim path,
// not the ceiling — the ceiling is unit-covered by TestParseCacheHint) until
// it elapses, at which point discovery resumes.
func TestRefreshTools_ServesToolsListFromCacheWithinTTL(t *testing.T) {
	advance := freezeClock(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	fake := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		WithFakeRejectLegacyHandshake(),
		WithFakeToolsListCacheHint(30000, "public"),
	)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-cache-ttl', 'cache-ttl-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	ctx := context.Background()
	registerDiff, err := reg.RefreshTools(ctx, serverID)
	if err != nil {
		t.Fatalf("RefreshTools (register): %v", err)
	}
	if got := len(fake.RequestsFor(methodToolsList)); got != 1 {
		t.Fatalf("tools/list requests after register = %d, want 1", got)
	}
	if registerDiff.ServedFromCache {
		t.Error("ServedFromCache = true on the first (real-fetch) refresh, want false")
	}

	diff, err := reg.RefreshTools(ctx, serverID)
	if err != nil {
		t.Fatalf("RefreshTools (cached, same instant): %v", err)
	}
	if got := len(fake.RequestsFor(methodToolsList)); got != 1 {
		t.Errorf("tools/list requests after cached refresh = %d, want still 1", got)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 || len(diff.Modified) != 0 {
		t.Errorf("ToolDiff on a cache hit = %+v, want empty", diff)
	}
	if !diff.ServedFromCache {
		t.Error("ServedFromCache = false on a cache hit, want true")
	}

	gotServer, err := store.GetMCPServer(ctx, serverID)
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	if gotServer.LastDiscoveredAt == nil {
		t.Error("last_discovered_at not written on a cache-hit refresh")
	}

	var toolCount int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_tools WHERE server_id = ?`, serverID).Scan(&toolCount); err != nil {
		t.Fatalf("count mcp_tools: %v", err)
	}
	if toolCount != 1 {
		t.Errorf("mcp_tools row count = %d, want 1 (unchanged by the cache hit)", toolCount)
	}

	advance(31 * time.Second)

	postTTLDiff, err := reg.RefreshTools(ctx, serverID)
	if err != nil {
		t.Fatalf("RefreshTools (post-TTL): %v", err)
	}
	if got := len(fake.RequestsFor(methodToolsList)); got != 2 {
		t.Errorf("tools/list requests after TTL expiry = %d, want 2", got)
	}
	if postTTLDiff.ServedFromCache {
		t.Error("ServedFromCache = true after TTL expiry, want false (a real fetch)")
	}
}

// TestRefreshTools_CacheHitDoesNotClearExistingDriftFlag is the F1(c)
// regression test (security review, cycle 2): has_drift must be a
// real-fetch-only signal. A cache-hit refresh's freshly-discovered tools are
// whatever the last real fetch already wrote to the DB, so a diff computed
// against that same DB state is (almost always) empty — before this fix,
// writing has_drift from that empty diff on every refresh, cache hit or not,
// would silently clear a drift flag a genuine prior real fetch had set.
func TestRefreshTools_CacheHitDoesNotClearExistingDriftFlag(t *testing.T) {
	freezeClock(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	// fake1 serves tool-a only; its hint is honored, so the first refresh
	// (the baseline discovery) also populates the tool-list cache.
	fake1 := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		WithFakeRejectLegacyHandshake(),
		WithFakeToolsListCacheHint(30000, "public"),
		WithFakeTools(Tool{Name: "tool-a", Description: "tool-a description", InputSchema: json.RawMessage(`{"type":"object"}`)}),
	)
	srv1 := httptest.NewServer(fake1)
	t.Cleanup(srv1.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-drift-cache', 'drift-cache-server', ?, ?) RETURNING id`,
		srv1.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	ctx := context.Background()
	baselineDiff, err := reg.RefreshTools(ctx, serverID)
	if err != nil {
		t.Fatalf("RefreshTools (baseline): %v", err)
	}
	if baselineDiff.ServedFromCache {
		t.Fatal("baseline refresh: ServedFromCache = true, want false (first discovery is always a real fetch)")
	}

	var hasDrift int64
	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift after baseline: %v", err)
	}
	if hasDrift != 0 {
		t.Fatalf("has_drift = %d after baseline discovery, want 0", hasDrift)
	}

	// fake2 serves tool-a + tool-b under the same cache-hint terms. Pointing
	// the server row at it changes serverConfig (the url), which forces the
	// next refresh to be a real fetch even though the previous cache entry
	// has not expired.
	fake2 := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		WithFakeRejectLegacyHandshake(),
		WithFakeToolsListCacheHint(30000, "public"),
		WithFakeTools(
			Tool{Name: "tool-a", Description: "tool-a description", InputSchema: json.RawMessage(`{"type":"object"}`)},
			Tool{Name: "tool-b", Description: "tool-b description", InputSchema: json.RawMessage(`{"type":"object"}`)},
		),
	)
	srv2 := httptest.NewServer(fake2)
	t.Cleanup(srv2.Close)

	if _, err := rawDB.Exec(`UPDATE mcp_servers SET url = ? WHERE id = ?`, srv2.URL, serverID); err != nil {
		t.Fatalf("update server url: %v", err)
	}

	driftDiff, err := reg.RefreshTools(ctx, serverID)
	if err != nil {
		t.Fatalf("RefreshTools (drift, real fetch against fake2): %v", err)
	}
	if driftDiff.ServedFromCache {
		t.Fatal("drift refresh: ServedFromCache = true, want false — the url change must force a real fetch")
	}
	if len(driftDiff.Added) != 1 || driftDiff.Added[0] != "tool-b" {
		t.Fatalf("drift refresh: Added = %v, want [tool-b]", driftDiff.Added)
	}

	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift after drift refresh: %v", err)
	}
	if hasDrift != 1 {
		t.Fatalf("has_drift = %d after adding tool-b, want 1", hasDrift)
	}
	if got := len(fake2.RequestsFor(methodToolsList)); got != 1 {
		t.Fatalf("fake2 tools/list requests = %d, want 1", got)
	}

	// Refresh again with no server-side or config change at all: this must be
	// a cache hit (fake2's hint is still within its 30s TTL and the frozen
	// clock has not advanced). The diff against the now-current DB state
	// (tool-a + tool-b) is empty — but has_drift must stay 1, not be cleared
	// back to 0 by this cache-hit refresh.
	cacheHitDiff, err := reg.RefreshTools(ctx, serverID)
	if err != nil {
		t.Fatalf("RefreshTools (cache hit after drift): %v", err)
	}
	if !cacheHitDiff.ServedFromCache {
		t.Fatal("post-drift refresh: ServedFromCache = false, want true (a cache hit)")
	}
	if len(cacheHitDiff.Added) != 0 || len(cacheHitDiff.Removed) != 0 || len(cacheHitDiff.Modified) != 0 {
		t.Errorf("cache-hit diff = %+v, want empty", cacheHitDiff)
	}
	if got := len(fake2.RequestsFor(methodToolsList)); got != 1 {
		t.Errorf("fake2 tools/list requests after cache-hit refresh = %d, want still 1", got)
	}

	if err := rawDB.QueryRow(`SELECT has_drift FROM mcp_servers WHERE id = ?`, serverID).Scan(&hasDrift); err != nil {
		t.Fatalf("query has_drift after cache-hit refresh: %v", err)
	}
	if hasDrift != 1 {
		t.Errorf("has_drift = %d after a cache-hit refresh, want still 1 — a cache hit must never clear an existing drift flag", hasDrift)
	}
}

// TestRefreshTools_LegacyServerNeverCachesEvenWhenHintPresent proves the
// client's own protocol pin — not the raw response payload — gates caching:
// a legacy-classified server sending a modern-shaped hint anyway is still
// never cached.
func TestRefreshTools_LegacyServerNeverCachesEvenWhenHintPresent(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	fake := NewFakeMCPServer(WithFakeMode(FakeLegacy), WithFakeToolsListCacheHint(30000, "public"))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-legacy-hint', 'legacy-hint-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	ctx := context.Background()
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (1st): %v", err)
	}
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (2nd): %v", err)
	}

	if got := len(fake.RequestsFor(methodToolsList)); got != 2 {
		t.Errorf("tools/list requests = %d, want 2 — a legacy-classified server must never be served from cache", got)
	}
}

// TestRefreshTools_ModernServerWithoutHintNeverCaches verifies that a modern
// server which simply never sends a cache hint gets exactly today's
// always-fetch behavior.
func TestRefreshTools_ModernServerWithoutHintNeverCaches(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	fake := NewFakeMCPServer(WithFakeMode(FakeModern))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-modern-nohint', 'modern-nohint-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	ctx := context.Background()
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (1st): %v", err)
	}
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (2nd): %v", err)
	}

	if got := len(fake.RequestsFor(methodToolsList)); got != 2 {
		t.Errorf("tools/list requests = %d, want 2 — no hint means no caching", got)
	}
}

// TestRefreshTools_ZeroTTLHintNeverServesFromCache verifies the spec's
// "ttlMs: 0 means immediately stale" rule: two refreshes at the exact same
// frozen instant still both hit the network.
func TestRefreshTools_ZeroTTLHintNeverServesFromCache(t *testing.T) {
	freezeClock(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	fake := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		WithFakeRejectLegacyHandshake(),
		WithFakeToolsListCacheHint(0, "public"),
	)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-zero-ttl', 'zero-ttl-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	ctx := context.Background()
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (1st): %v", err)
	}
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (2nd): %v", err)
	}

	if got := len(fake.RequestsFor(methodToolsList)); got != 2 {
		t.Errorf("tools/list requests = %d, want 2 — ttlMs: 0 is \"immediately stale\" and must never serve a cache hit", got)
	}
}

// TestRefreshTools_PrivateScopeNeverCaches verifies the fail-closed decision
// on cacheScope: "private" (security review, cycle 2, F2): even a
// well-formed, positive-TTL hint never installs a cache entry when its Scope
// is "private", so a "private" hint gets exactly today's always-fetch
// behavior — the same outcome as an absent hint, a legacy server, or
// ttlMs: 0, but for a different reason (see discoverToolsCached's doc).
func TestRefreshTools_PrivateScopeNeverCaches(t *testing.T) {
	freezeClock(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	fake := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		WithFakeRejectLegacyHandshake(),
		WithFakeToolsListCacheHint(30000, "private"),
	)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-private-scope', 'private-scope-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	ctx := context.Background()
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (1st): %v", err)
	}
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (2nd, inside the TTL): %v", err)
	}

	if got := len(fake.RequestsFor(methodToolsList)); got != 2 {
		t.Errorf("tools/list requests = %d, want 2 — cacheScope \"private\" must never be served from cache", got)
	}
}

// TestRefreshTools_CacheInvalidatedByAuthHeaderChange verifies ADR-039's
// most at-risk invariant: rotating a server's auth headers invalidates any
// cached tool catalog immediately, even from inside a still-live TTL.
func TestRefreshTools_CacheInvalidatedByAuthHeaderChange(t *testing.T) {
	freezeClock(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	testKey := mustTestKey(t)
	reg, store := newTestRegistryWithKey(t, testKey)
	rawDB := store.DB()

	fake := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		WithFakeRejectLegacyHandshake(),
		WithFakeToolsListCacheHint(30000, "public"),
	)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-auth-invalidate', 'auth-invalidate-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	ctx := context.Background()
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (1st): %v", err)
	}
	if got := len(fake.RequestsFor(methodToolsList)); got != 1 {
		t.Fatalf("tools/list requests after 1st refresh = %d, want 1", got)
	}

	ciphertext := mustEncryptHeaders(t, testKey, []AuthHeader{{Name: "X-Api-Key", Value: "rotated"}})
	if _, err := rawDB.Exec(`UPDATE mcp_servers SET auth_headers_encrypted = ? WHERE id = ?`, *ciphertext, serverID); err != nil {
		t.Fatalf("update auth headers: %v", err)
	}

	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (2nd, inside the TTL): %v", err)
	}
	if got := len(fake.RequestsFor(methodToolsList)); got != 2 {
		t.Errorf("tools/list requests after auth header rotation = %d, want 2 — a rotated header must invalidate the cache even inside the TTL", got)
	}
}

// TestRefreshTools_DiscoveryErrorDoesNotPopulateCache verifies that a failed
// tools/list never plants a cache entry: the failure must not silently hide
// behind a stale-but-absent cache, and the next attempt must be a real
// network call.
func TestRefreshTools_DiscoveryErrorDoesNotPopulateCache(t *testing.T) {
	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	var failToolsList atomic.Bool
	failToolsList.Store(true)
	var toolsListCount atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		method, _ := req["method"].(string)
		switch method {
		case methodServerDiscover:
			writeJSON(w, map[string]any{
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"resultType":        "complete",
					"supportedVersions": []string{ProtocolVersion20260728},
				},
			})
		case methodToolsList:
			toolsListCount.Add(1)
			if failToolsList.Load() {
				writeJSON(w, map[string]any{
					"jsonrpc": "2.0", "id": req["id"],
					"error": map[string]any{"code": -32000, "message": "discovery unavailable"},
				})
				return
			}
			writeJSON(w, map[string]any{
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "tool-a", "description": "d", "inputSchema": map[string]any{"type": "object"}},
					},
					"ttlMs":      30000,
					"cacheScope": "public",
				},
			})
		default:
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{}})
		}
	}))
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-discovery-err', 'discovery-err-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	ctx := context.Background()
	if _, err := reg.RefreshTools(ctx, serverID); err == nil {
		t.Fatal("expected RefreshTools to return an error while tools/list fails")
	}

	failToolsList.Store(false)
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (after recovery): %v", err)
	}
	if got := toolsListCount.Load(); got != 2 {
		t.Errorf("tools/list requests = %d, want 2 — a failed discovery must not populate the cache", got)
	}
}

// TestForgetServer_DropsCachedClientAndToolList verifies ForgetServer evicts
// both caches for a server: a subsequent client resolve rebuilds, and a
// subsequent refresh re-lists even from inside a still-live TTL.
func TestForgetServer_DropsCachedClientAndToolList(t *testing.T) {
	freezeClock(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	reg, store := newTestRegistry(t)
	rawDB := store.DB()

	fake := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		WithFakeRejectLegacyHandshake(),
		WithFakeToolsListCacheHint(30000, "public"),
	)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var serverID string
	if err := rawDB.QueryRow(
		`INSERT INTO mcp_servers (id, name, url, created_at) VALUES ('srv-forget', 'forget-server', ?, ?) RETURNING id`,
		srv.URL, "2024-01-01T00:00:00Z",
	).Scan(&serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	ctx := context.Background()
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (register): %v", err)
	}
	if got := len(fake.RequestsFor(methodToolsList)); got != 1 {
		t.Fatalf("tools/list requests after register = %d, want 1", got)
	}

	srvRow, err := store.GetMCPServer(ctx, serverID)
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	client1 := reg.clientForServer(srvRow)

	reg.ForgetServer(serverID)

	client2 := reg.clientForServer(srvRow)
	if client2 == client1 {
		t.Error("ForgetServer did not evict the cached *Client")
	}

	// Still inside the tool-list TTL: without ForgetServer this would be a
	// cache hit (see TestRefreshTools_ServesToolsListFromCacheWithinTTL).
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		t.Fatalf("RefreshTools (after ForgetServer): %v", err)
	}
	if got := len(fake.RequestsFor(methodToolsList)); got != 2 {
		t.Errorf("tools/list requests after ForgetServer = %d, want 2", got)
	}
}

// TestRegistryCache_ConcurrentResolveIsRaceFree drives N concurrent
// ResolveToolByName calls against the same tool, mirroring
// TestRefreshTools_ConcurrentRefreshesCannotRaceThePin's concurrency-test
// shape. clientForServer holds registryCache.mu across its entire miss path
// (check, build, store), so exactly one *Client is ever built regardless of
// how many goroutines race to resolve first — asserted here, not just
// "clean under -race".
func TestRegistryCache_ConcurrentResolveIsRaceFree(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "my-tool", "description": "a tool", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	const n = 20
	clients := make([]*Client, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			client, _, err := reg.ResolveToolByName(context.Background(), "my-server.my-tool")
			if err != nil {
				t.Errorf("ResolveToolByName: %v", err)
				return
			}
			clients[i] = client
		}(i)
	}
	wg.Wait()

	first := clients[0]
	if first == nil {
		t.Fatal("no client resolved")
	}
	for i, c := range clients {
		if c != first {
			t.Errorf("clients[%d] has a different identity than clients[0]; want exactly one *Client built", i)
		}
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

func TestValidateCallTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name    string
		n       int64
		wantErr bool
	}{
		{"negative", -1, true},
		{"zero", 0, true},
		{"floor", 1, false},
		{"mid", 120, false},
		{"ceiling", 600, false},
		{"just over ceiling", 601, true},
		{"max int64", math.MaxInt64, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCallTimeoutSeconds(tc.n)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateCallTimeoutSeconds(%d) error = %v, wantErr %v", tc.n, err, tc.wantErr)
			}
		})
	}
}

func TestCallTimeoutFor(t *testing.T) {
	seconds := func(n int64) *int64 { return &n }

	tests := []struct {
		name       string
		mcpTimeout time.Duration
		override   *int64
		want       time.Duration
	}{
		{"no override honors WithMCPTimeout", 45 * time.Second, nil, 45 * time.Second},
		{"no override falls back to the client default", 0, nil, defaultProbeTimeout},
		{"override wins over WithMCPTimeout", 45 * time.Second, seconds(120), 120 * time.Second},
		{"override zero is treated as unset", 45 * time.Second, seconds(0), 45 * time.Second},
		{"negative hand-edited override is treated as unset", 45 * time.Second, seconds(-5), 45 * time.Second},
		{"override above the ceiling is clamped", 45 * time.Second, seconds(99999), time.Duration(MaxCallTimeoutSeconds) * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var opts []RegistryOption
			if tc.mcpTimeout > 0 {
				opts = append(opts, WithMCPTimeout(tc.mcpTimeout))
			}
			r := NewRegistry(nil, opts...)
			srv := db.McpServer{ID: "srv", Name: "server", Url: "http://example.invalid", CallTimeoutSeconds: tc.override}

			cl := r.newClientForServer(srv)
			if cl.httpClient.Timeout != tc.want {
				t.Errorf("httpClient.Timeout = %v, want %v", cl.httpClient.Timeout, tc.want)
			}
		})
	}
}

// TestCallTimeout_StreamClientStillUnbounded pins that a per-server call
// timeout override never leaks into the events/listen stream client:
// streamHTTPClient forces Timeout 0 unconditionally, regardless of what
// WithTimeout set on the underlying httpClient (issue #939).
func TestCallTimeout_StreamClientStillUnbounded(t *testing.T) {
	override := int64(120)
	pluginInstanceID := "instance-1"
	protocol := ProtocolVersion20260728
	srv := db.McpServer{
		ID:                 "srv",
		Name:               "managed-server",
		Url:                "http://example.invalid",
		PluginInstanceID:   &pluginInstanceID,
		ProtocolVersion:    &protocol,
		CallTimeoutSeconds: &override,
	}

	r := NewRegistry(nil)
	cl := r.newClientForServer(srv)

	if cl.httpClient.Timeout != 120*time.Second {
		t.Fatalf("httpClient.Timeout = %v, want 120s", cl.httpClient.Timeout)
	}
	if got := cl.streamHTTPClient().Timeout; got != 0 {
		t.Errorf("streamHTTPClient().Timeout = %v, want 0 (the override must never bound a long-lived stream)", got)
	}
}

// blockingCallLegacyServer starts a minimal legacy MCP server (server/discover
// 404s, like sessionCountingLegacyServer) whose tools/call handler signals
// entered once a call arrives, then blocks until release is closed or the
// request's context is cancelled -- letting a test hold a call open for as
// long as it needs to prove a timeout behavior without a real wall-clock
// sleep.
func blockingCallLegacyServer(t *testing.T, toolName string) (srv *httptest.Server, entered chan struct{}, release chan struct{}) {
	t.Helper()
	entered = make(chan struct{}, 1)
	release = make(chan struct{})

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
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
			entered <- struct{}{}
			select {
			case <-release:
				writeJSON(w, map[string]any{
					"jsonrpc": "2.0", "id": req["id"],
					"result": map[string]any{
						"content": []map[string]any{{"type": "text", "text": "ok"}},
						"isError": false,
					},
				})
			case <-r.Context().Done():
				return
			}
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("404 page not found")) //nolint:errcheck
		}
	}))
	return srv, entered, release
}

// TestCallTimeout_OverrideServerOutlastsDefaultServer is the acceptance
// behaviour scaled down to run without a wall-clock wait (signal-don't-poll):
// two mcp_servers rows point at the same slow server, one with a
// call_timeout_seconds override and one without. The unset row's call fails
// against the (deliberately tiny) instance default while the override row's
// call, held open for the same wall-clock duration, still succeeds -- proving
// the override reaches the next resolve with no restart.
func TestCallTimeout_OverrideServerOutlastsDefaultServer(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	reg := NewRegistry(store.Queries(), WithMCPTimeout(200*time.Millisecond))

	srv, entered, release := blockingCallLegacyServer(t, "my-tool")
	t.Cleanup(srv.Close)
	var releaseOnce sync.Once
	// Registered after srv.Close's cleanup above, so LIFO runs this FIRST:
	// release must be closed before Close is called, or Close blocks forever
	// waiting for the held-open tools/call request to finish.
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "slow-default", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest(slow-default): %v", err)
	}
	overrideServerID, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "slow-override", srv.URL)
	if err != nil {
		t.Fatalf("RegisterServerForTest(slow-override): %v", err)
	}
	if _, err := store.DB().Exec(`UPDATE mcp_servers SET call_timeout_seconds = 5 WHERE id = ?`, overrideServerID); err != nil {
		t.Fatalf("set call_timeout_seconds: %v", err)
	}

	defaultClient, defaultToolName, err := reg.ResolveToolByName(context.Background(), "slow-default.my-tool")
	if err != nil {
		t.Fatalf("ResolveToolByName(slow-default): %v", err)
	}
	overrideClient, overrideToolName, err := reg.ResolveToolByName(context.Background(), "slow-override.my-tool")
	if err != nil {
		t.Fatalf("ResolveToolByName(slow-override): %v", err)
	}

	type callOutcome struct {
		err error
	}
	overrideDone := make(chan callOutcome, 1)
	go func() {
		_, err := overrideClient.CallTool(context.Background(), overrideToolName, nil, CallOptions{})
		overrideDone <- callOutcome{err: err}
	}()

	// Wait for the override call to actually be held server-side before
	// starting the default call, so the two genuinely overlap.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the override call to reach the server")
	}

	_, err = defaultClient.CallTool(context.Background(), defaultToolName, nil, CallOptions{})
	if err == nil {
		t.Fatal("expected the unset-timeout call to fail against the 200ms instance default, got nil error")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("default-server call error = %v, want a net.Error with Timeout() true", err)
	}

	releaseOnce.Do(func() { close(release) })

	select {
	case outcome := <-overrideDone:
		if outcome.err != nil {
			t.Fatalf("override-server call error = %v, want success", outcome.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the override call to complete")
	}
}

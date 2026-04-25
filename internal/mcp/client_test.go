package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// makeServer starts an httptest server that calls handler and registers cleanup.
func makeServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// writeJSON is a small helper used by test handlers to write a JSON response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func TestDiscoverTools_HappyPath(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session")
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}})
			return
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"tools": []map[string]any{
					{"name": "tool-alpha", "description": "first tool", "inputSchema": map[string]any{"type": "object"}},
					{"name": "tool-beta", "description": "second tool", "inputSchema": map[string]any{"type": "object"}},
				},
			},
		})
	})

	c := NewClient(srv.URL)
	tools, err := c.DiscoverTools(context.Background())
	if err != nil {
		t.Fatalf("DiscoverTools: unexpected error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	if tools[0].Name != "tool-alpha" {
		t.Errorf("tools[0].Name = %q, want %q", tools[0].Name, "tool-alpha")
	}
	if tools[1].Name != "tool-beta" {
		t.Errorf("tools[1].Name = %q, want %q", tools[1].Name, "tool-beta")
	}
}

func TestDiscoverTools_JSONRPCError(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"error": map[string]any{
				"code":    -32601,
				"message": "method not found",
			},
		})
	})

	c := NewClient(srv.URL)
	_, err := c.DiscoverTools(context.Background())
	if err == nil {
		t.Fatal("expected non-nil error for JSON-RPC error response, got nil")
	}
	if !strings.Contains(err.Error(), "-32601") {
		t.Errorf("error %q does not contain code -32601", err.Error())
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("error %q does not contain message", err.Error())
	}
}

func TestDiscoverTools_Non200Response(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})

	c := NewClient(srv.URL)
	_, err := c.DiscoverTools(context.Background())
	if err == nil {
		t.Fatal("expected non-nil error for 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not contain '500'", err.Error())
	}
}

func TestDiscoverTools_ContextCancellation(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Should not be reached.
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	})

	c := NewClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	_, err := c.DiscoverTools(ctx)
	if err == nil {
		t.Fatal("expected non-nil error for cancelled context, got nil")
	}
}

func TestCallTool_HappyPath(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}

		// CallTool performs the MCP handshake before the actual tool call.
		// Route on method so initialize and notifications/initialized succeed.
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session")
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}})
			return
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
			return
		}

		// Verify the tool call contains the expected tool name.
		params, _ := req["params"].(map[string]any)
		if params["name"] != "my-tool" {
			http.Error(w, "wrong tool name", http.StatusBadRequest)
			return
		}

		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "hello from tool"},
				},
				"isError": false,
			},
		})
	})

	c := NewClient(srv.URL)
	result, err := c.CallTool(context.Background(), "my-tool", map[string]any{"key": "val"})
	if err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("IsError = true, want false")
	}
	if result.Output == nil {
		t.Errorf("Output is nil, want non-nil")
	}
}

func TestCallTool_IsErrorTrue(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "tool execution failed"},
				},
				"isError": true,
			},
		})
	})

	c := NewClient(srv.URL)
	result, err := c.CallTool(context.Background(), "failing-tool", nil)
	if err != nil {
		t.Fatalf("CallTool: unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("IsError = false, want true")
	}
}

func TestCallTool_JSONRPCError(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"error": map[string]any{
				"code":    -32602,
				"message": "invalid params",
			},
		})
	})

	c := NewClient(srv.URL)
	_, err := c.CallTool(context.Background(), "some-tool", nil)
	if err == nil {
		t.Fatal("expected non-nil error for JSON-RPC error response, got nil")
	}
	if !strings.Contains(err.Error(), "-32602") {
		t.Errorf("error %q does not contain code -32602", err.Error())
	}
}

func TestCallTool_Non200Response(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	})

	c := NewClient(srv.URL)
	_, err := c.CallTool(context.Background(), "some-tool", nil)
	if err == nil {
		t.Fatal("expected non-nil error for 502 response, got nil")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error %q does not contain '502'", err.Error())
	}
}

func TestCallTool_ContextCancellation(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Should not be reached.
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []any{},
				"isError": false,
			},
		})
	})

	c := NewClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	_, err := c.CallTool(ctx, "some-tool", nil)
	if err == nil {
		t.Fatal("expected non-nil error for cancelled context, got nil")
	}
}

func TestNewClient_DefaultTimeout(t *testing.T) {
	c := NewClient("http://example.com")
	if c.httpClient.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want %v", c.httpClient.Timeout, 30*time.Second)
	}
}

func TestNewClient_WithTimeout(t *testing.T) {
	c := NewClient("http://example.com", WithTimeout(5*time.Second))
	if c.httpClient.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want %v", c.httpClient.Timeout, 5*time.Second)
	}
}

func TestNewClient_WithHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 10 * time.Second}
	c := NewClient("http://example.com", WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Errorf("httpClient pointer mismatch: got %p, want %p", c.httpClient, custom)
	}
}

func TestNewClient_WithHTTPClientAndTimeout(t *testing.T) {
	custom := &http.Client{Timeout: 10 * time.Second}
	c := NewClient("http://example.com", WithHTTPClient(custom), WithTimeout(7*time.Second))
	// WithTimeout should mutate the custom client's timeout.
	if c.httpClient.Timeout != 7*time.Second {
		t.Errorf("timeout = %v, want %v", c.httpClient.Timeout, 7*time.Second)
	}
	// Confirm the injected client is still the one in use.
	if c.httpClient != custom {
		t.Errorf("httpClient pointer changed after WithTimeout")
	}
}

func TestDiscoverTools_WithCustomClient(t *testing.T) {
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"tools": []map[string]any{
					{"name": "injected-tool", "description": "via custom client", "inputSchema": map[string]any{"type": "object"}},
				},
			},
		})
	})

	c := NewClient(srv.URL, WithHTTPClient(srv.Client()))
	tools, err := c.DiscoverTools(context.Background())
	if err != nil {
		t.Fatalf("DiscoverTools: unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	if tools[0].Name != "injected-tool" {
		t.Errorf("tools[0].Name = %q, want %q", tools[0].Name, "injected-tool")
	}
}

// routingHandler returns an http.HandlerFunc that routes on the JSON-RPC method.
// initExtra is called (if non-nil) after setting the session header, before writing
// the initialize response — useful for side effects like incrementing a counter.
func routingHandler(t *testing.T, initExtra func(), toolCallHandler http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			if initExtra != nil {
				initExtra()
			}
			w.Header().Set("Mcp-Session-Id", "test-session")
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		default:
			toolCallHandler(w, r)
		}
	}
}

func successToolCallHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"isError": false,
		},
	})
}

func TestEnsureSession_CachesSessionID(t *testing.T) {
	var initCount atomic.Int32

	srv := makeServer(t, routingHandler(t, func() { initCount.Add(1) }, successToolCallHandler))

	c := NewClient(srv.URL)

	if _, err := c.CallTool(context.Background(), "tool-x", nil); err != nil {
		t.Fatalf("first CallTool: %v", err)
	}
	if _, err := c.CallTool(context.Background(), "tool-x", nil); err != nil {
		t.Fatalf("second CallTool: %v", err)
	}

	if got := initCount.Load(); got != 1 {
		t.Errorf("initialize called %d times, want 1 (session should be cached)", got)
	}
}

func TestEnsureSession_RetriesOn401(t *testing.T) {
	var initCount atomic.Int32
	var callCount atomic.Int32

	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			n := initCount.Add(1)
			w.Header().Set("Mcp-Session-Id", "session-"+string(rune('0'+n)))
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		default:
			n := callCount.Add(1)
			if n == 1 {
				// First tools/call simulates an expired session.
				http.Error(w, "session expired", http.StatusUnauthorized)
				return
			}
			successToolCallHandler(w, r)
		}
	})

	c := NewClient(srv.URL)
	if _, err := c.CallTool(context.Background(), "tool-x", nil); err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}

	if got := initCount.Load(); got != 2 {
		t.Errorf("initialize called %d times, want 2 (initial + retry after 401)", got)
	}
}

func TestEnsureSession_ConcurrentCalls(t *testing.T) {
	var initCount atomic.Int32

	srv := makeServer(t, routingHandler(t, func() { initCount.Add(1) }, successToolCallHandler))

	c := NewClient(srv.URL)

	const goroutines = 5
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			if _, err := c.CallTool(context.Background(), "tool-x", nil); err != nil {
				t.Errorf("CallTool: %v", err)
			}
		}()
	}
	wg.Wait()

	// With double-checked locking, at most N goroutines may each call initialize
	// once if they all race before the session is stored. Any value in [1, 5] is
	// correct; the important thing is that there are no data races (verified by
	// the race detector when tests run with -race).
	if got := initCount.Load(); got < 1 || got > goroutines {
		t.Errorf("initialize called %d times, want between 1 and %d", got, goroutines)
	}
}

// TestPostRaw_InjectsAuthHeaders verifies that WithAuthHeaders causes the
// configured headers to be sent on every outbound request, and that the
// Mcp-Session-Id is still set correctly.
func TestPostRaw_InjectsAuthHeaders(t *testing.T) {
	var capturedHeader string
	var capturedSession string

	srv := makeServer(t, routingHandler(t, nil, func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("X-Api-Key")
		capturedSession = r.Header.Get("Mcp-Session-Id")
		successToolCallHandler(w, r)
	}))

	c := NewClient(srv.URL, WithAuthHeaders([]AuthHeader{
		{Name: "X-Api-Key", Value: "sk-test-123"},
	}))

	if _, err := c.CallTool(context.Background(), "tool-x", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if capturedHeader != "sk-test-123" {
		t.Errorf("X-Api-Key = %q, want %q", capturedHeader, "sk-test-123")
	}
	if capturedSession != "test-session" {
		t.Errorf("Mcp-Session-Id = %q, want %q", capturedSession, "test-session")
	}
}

// TestPostRaw_NoAuthHeaders_BackwardCompat verifies that a Client with no auth
// headers configured behaves identically to before this feature was added.
func TestPostRaw_NoAuthHeaders_BackwardCompat(t *testing.T) {
	var gotExtraHeader string

	srv := makeServer(t, routingHandler(t, nil, func(w http.ResponseWriter, r *http.Request) {
		gotExtraHeader = r.Header.Get("X-Custom")
		successToolCallHandler(w, r)
	}))

	// No WithAuthHeaders — default client.
	c := NewClient(srv.URL)

	if _, err := c.CallTool(context.Background(), "tool-x", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if gotExtraHeader != "" {
		t.Errorf("X-Custom = %q, want empty (no auth headers configured)", gotExtraHeader)
	}
}

// TestPostRaw_AuthHeaderCannotOverrideSessionID verifies that even if an
// operator somehow configures "Mcp-Session-Id" as an auth header (bypassing
// the validator), the client-managed value wins because it is set after the
// auth headers loop in postRaw.
func TestPostRaw_AuthHeaderCannotOverrideSessionID(t *testing.T) {
	var capturedSession string

	srv := makeServer(t, routingHandler(t, nil, func(w http.ResponseWriter, r *http.Request) {
		capturedSession = r.Header.Get("Mcp-Session-Id")
		successToolCallHandler(w, r)
	}))

	// Bypass the validator by setting the header directly via the option.
	c := NewClient(srv.URL, WithAuthHeaders([]AuthHeader{
		{Name: "Mcp-Session-Id", Value: "injected-session"},
	}))

	if _, err := c.CallTool(context.Background(), "tool-x", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	// The server assigns "test-session" in routingHandler; the client stores and
	// resends it. The auth header's "injected-session" must NOT win.
	if capturedSession == "injected-session" {
		t.Errorf("Mcp-Session-Id = %q: auth header overwrote client-managed session ID", capturedSession)
	}
	if capturedSession != "test-session" {
		t.Errorf("Mcp-Session-Id = %q, want %q (client-managed)", capturedSession, "test-session")
	}
}

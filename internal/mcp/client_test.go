package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

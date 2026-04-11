package trigger_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// minimalWebhookPolicy is the smallest YAML that parses cleanly with trigger
// type webhook and the default concurrency (skip). Centralised here so all
// trigger tests share a single definition.
const minimalWebhookPolicy = testutil.MinimalWebhookPolicy

// insertTestPolicy inserts a webhook policy with the given ID and YAML.
func insertTestPolicy(t *testing.T, store *db.Store, policyID, yaml string) {
	t.Helper()
	testutil.InsertPolicy(t, store, policyID, "policy-"+policyID, "webhook", yaml)
}

// insertTestRun inserts a run with the given IDs and status.
func insertTestRun(t *testing.T, store *db.Store, runID, policyID string, status model.RunStatus) {
	t.Helper()
	testutil.InsertRun(t, store, runID, policyID, status)
}

// newStubMCPServer starts an httptest.Server that handles MCP JSON-RPC over
// HTTP. It responds to tools/list with a single "read_data" tool and to all
// other methods with a stub result.
func newStubMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		method, _ := req["method"].(string)
		switch method {
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "read_data",
						"description": "reads data",
						"inputSchema": map[string]any{
							"type": "object", "properties": map[string]any{},
						},
					}},
				},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "stub result"}},
					"isError": false,
				},
			})
		}
	}))
}

// setupIntegrationFixture opens a temp SQLite store, starts a stub MCP server,
// and registers it with a fresh Registry. Cleanup for both is registered via
// t.Cleanup — callers do not need to close anything manually.
func setupIntegrationFixture(t *testing.T) (*db.Store, *mcp.Registry) {
	t.Helper()
	store := testutil.NewTestStore(t)
	mcpSrv := newStubMCPServer(t)
	t.Cleanup(mcpSrv.Close)
	registry := mcp.NewRegistry(store.Queries())
	if err := registry.RegisterServer(context.Background(), "stub-server", mcpSrv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}
	return store, registry
}

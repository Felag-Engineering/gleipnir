package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
)

// newMCPHandlerStore opens a fresh migrated store for MCP handler tests.
func newMCPHandlerStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// newMCPRouter wires a chi router with the MCP handler, mirroring how
// NewRouter mounts the routes in production.
func newMCPRouter(store *db.Store, registry *mcp.Registry) http.Handler {
	r := chi.NewRouter()
	h := api.NewMCPHandler(store, registry)
	r.Get("/servers", h.List)
	r.Post("/servers", h.Create)
	r.Delete("/servers/{id}", h.Delete)
	r.Post("/servers/{id}/discover", h.Discover)
	return r
}

// makeFakeMCPServer starts an httptest.Server that returns a tools/list JSON-RPC
// response containing the provided tool names. Follows the same pattern as
// makeMCPServer in internal/mcp/registry_test.go.
func makeFakeMCPServer(t *testing.T, toolNames []string) *httptest.Server {
	t.Helper()
	tools := make([]map[string]any, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, map[string]any{
			"name":        name,
			"description": name + " description",
			"inputSchema": map[string]any{"type": "object"},
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"tools": tools},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// insertTestMCPServer inserts an MCP server row directly via the store.
func insertTestMCPServer(t *testing.T, s *db.Store, name, url string) string {
	t.Helper()
	id := model.NewULID()
	_, err := s.CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:        id,
		Name:      name,
		Url:       url,
		CreatedAt: "2024-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("insertTestMCPServer %s: %v", name, err)
	}
	return id
}

func TestMCPServerListHandler(t *testing.T) {
	t.Run("empty list returns [] not null", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/servers")
		if err != nil {
			t.Fatalf("GET /servers: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if string(envelope.Data) != "[]" {
			t.Errorf("data = %s, want []", envelope.Data)
		}
	})

	t.Run("list after insert returns server with fields", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())
		insertTestMCPServer(t, store, "my-server", "http://localhost:9999")

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/servers")
		if err != nil {
			t.Fatalf("GET /servers: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("len(data) = %d, want 1", len(envelope.Data))
		}
		if envelope.Data[0].Name != "my-server" {
			t.Errorf("name = %q, want my-server", envelope.Data[0].Name)
		}
		if envelope.Data[0].URL != "http://localhost:9999" {
			t.Errorf("url = %q, want http://localhost:9999", envelope.Data[0].URL)
		}
	})
}

func TestMCPServerCreateHandler(t *testing.T) {
	t.Run("valid data with reachable MCP server returns 201 with server data", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())

		fakeMCP := makeFakeMCPServer(t, []string{"tool-a"})
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"name": "test-server", "url": fakeMCP.URL})
		resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				ID               string  `json:"id"`
				Name             string  `json:"name"`
				URL              string  `json:"url"`
				LastDiscoveredAt *string `json:"last_discovered_at"`
				DiscoveryError   *string `json:"discovery_error"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.ID == "" {
			t.Error("expected non-empty id")
		}
		if envelope.Data.Name != "test-server" {
			t.Errorf("name = %q, want test-server", envelope.Data.Name)
		}
		if envelope.Data.DiscoveryError != nil {
			t.Errorf("discovery_error = %q, want nil", *envelope.Data.DiscoveryError)
		}
		if envelope.Data.LastDiscoveredAt == nil {
			t.Error("expected last_discovered_at to be set after successful discovery")
		}
	})

	t.Run("valid data with unreachable MCP server returns 201 with discovery_error", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())

		// Start and immediately close so URL is valid but unreachable.
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadURL := dead.URL
		dead.Close()

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"name": "unreachable-server", "url": deadURL})
		resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				ID             string  `json:"id"`
				Name           string  `json:"name"`
				DiscoveryError *string `json:"discovery_error"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.ID == "" {
			t.Error("expected non-empty id")
		}
		if envelope.Data.DiscoveryError == nil {
			t.Error("expected discovery_error to be set, got nil")
		}
	})

	t.Run("missing name returns 400", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"url": "http://localhost:9999"})
		resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("missing url returns 400", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"name": "test-server"})
		resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("duplicate name returns 409", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())
		insertTestMCPServer(t, store, "existing-server", "http://localhost:9999")

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"name": "existing-server", "url": "http://localhost:8888"})
		resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
	})
}

func TestMCPServerDeleteHandler(t *testing.T) {
	t.Run("delete existing server with no policy refs returns 204", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())
		id := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/servers/"+id, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /servers/%s: %v", id, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}
	})

	t.Run("delete non-existent server returns 404", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/servers/does-not-exist", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /servers/does-not-exist: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("delete server referenced by policy returns 409", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())
		id := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")

		// Insert a policy that references a tool from this server.
		policyYAML := `
name: ref-policy
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: my-server.some_tool
agent:
  task: test
`
		_, err := store.CreatePolicy(context.Background(), db.CreatePolicyParams{
			ID:          model.NewULID(),
			Name:        "ref-policy",
			TriggerType: "webhook",
			Yaml:        policyYAML,
			CreatedAt:   "2024-01-01T00:00:00Z",
			UpdatedAt:   "2024-01-01T00:00:00Z",
		})
		if err != nil {
			t.Fatalf("CreatePolicy: %v", err)
		}

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/servers/"+id, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /servers/%s: %v", id, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}

		var envelope struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !strings.Contains(envelope.Detail, "ref-policy") {
			t.Errorf("detail %q should mention ref-policy", envelope.Detail)
		}
	})
}

func TestMCPServerDiscoverHandler(t *testing.T) {
	t.Run("discover existing server returns 200 with diff", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())

		fakeMCP := makeFakeMCPServer(t, []string{"tool-a", "tool-b"})
		id := insertTestMCPServer(t, store, "my-server", fakeMCP.URL)

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/servers/"+id+"/discover", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /servers/%s/discover: %v", id, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				Added    []string `json:"added"`
				Removed  []string `json:"removed"`
				Modified []string `json:"modified"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		// First discovery: both tools are "added".
		if len(envelope.Data.Added) != 2 {
			t.Errorf("added = %v, want [tool-a tool-b]", envelope.Data.Added)
		}
		if envelope.Data.Removed == nil {
			t.Error("removed must not be null — want empty array")
		}
		if envelope.Data.Modified == nil {
			t.Error("modified must not be null — want empty array")
		}
	})

	t.Run("discover non-existent server returns 404", func(t *testing.T) {
		store := newMCPHandlerStore(t)
		registry := mcp.NewRegistry(store.DB())
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/servers/does-not-exist/discover", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /servers/does-not-exist/discover: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})
}

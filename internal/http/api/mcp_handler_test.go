package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/api"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// newMCPRouter wires a chi router with the MCP handler, mirroring how
// NewRouter mounts the routes in production. encKey may be nil to simulate
// an unconfigured encryption key.
func newMCPRouter(store *db.Store, registry *mcp.Registry, encKey ...[]byte) http.Handler {
	var key []byte
	if len(encKey) > 0 {
		key = encKey[0]
	}
	r := chi.NewRouter()
	h := api.NewMCPHandler(store, registry, key)
	r.Get("/servers", h.List)
	r.Post("/servers", h.Create)
	// /servers/test must be registered before /servers/{id} so chi does not
	// capture "test" as an id parameter.
	r.Post("/servers/test", h.TestConnection)
	r.Delete("/servers/{id}", h.Delete)
	r.Put("/servers/{id}", h.Update)
	r.Put("/servers/{id}/headers/{name}", h.SetAuthHeader)
	r.Delete("/servers/{id}/headers/{name}", h.DeleteAuthHeader)
	r.Post("/servers/{id}/discover", h.Discover)
	r.Get("/servers/{id}/tools", h.ListTools)
	return r
}

// insertTestMCPTool inserts an MCP tool row directly via the store.
func insertTestMCPTool(t *testing.T, s *db.Store, serverID, name string) string {
	t.Helper()
	id := model.NewULID()
	_, err := s.UpsertMCPTool(context.Background(), db.UpsertMCPToolParams{
		ID:          id,
		ServerID:    serverID,
		Name:        name,
		Description: name + " description",
		InputSchema: `{"type":"object"}`,
		CreatedAt:   "2024-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("insertTestMCPTool %s: %v", name, err)
	}
	return id
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
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
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
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
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
				ID       string `json:"id"`
				Name     string `json:"name"`
				URL      string `json:"url"`
				HasDrift bool   `json:"has_drift"`
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
		if envelope.Data[0].HasDrift {
			t.Errorf("has_drift = true, want false for a freshly inserted server")
		}
	})
}

func TestMCPServerCreateHandler(t *testing.T) {
	t.Run("valid data with reachable MCP server returns 201 with server data", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

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
		wantLocation := "/api/v1/mcp/servers/" + envelope.Data.ID
		if loc := resp.Header.Get("Location"); loc != wantLocation {
			t.Errorf("Location = %q, want %q", loc, wantLocation)
		}
	})

	t.Run("valid data with unreachable MCP server returns 201 with discovery_error", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

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
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
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
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
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
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
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
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
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
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
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
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		id := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")

		// Insert a policy that references a tool from this server.
		policyYAML := `
name: ref-policy
trigger:
  type: webhook
capabilities:
  tools:
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
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

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

		// First discovery establishes the baseline: has_drift must be false even
		// though both tools appear in diff.Added. Drift only applies once a
		// baseline exists (i.e. tools were present in the DB before the refresh).
		listResp, err := http.Get(srv.URL + "/servers")
		if err != nil {
			t.Fatalf("GET /servers: %v", err)
		}
		defer listResp.Body.Close()

		var listEnvelope struct {
			Data []struct {
				ID       string `json:"id"`
				HasDrift bool   `json:"has_drift"`
			} `json:"data"`
		}
		if err := json.NewDecoder(listResp.Body).Decode(&listEnvelope); err != nil {
			t.Fatalf("decode list response: %v", err)
		}
		if len(listEnvelope.Data) != 1 {
			t.Fatalf("list len = %d, want 1", len(listEnvelope.Data))
		}
		if listEnvelope.Data[0].HasDrift {
			t.Errorf("has_drift = true after first discovery, want false")
		}
	})

	t.Run("discover non-existent server returns 404", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
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

func TestMCPServerTestConnectionHandler(t *testing.T) {
	type testResult struct {
		OK        bool     `json:"ok"`
		ToolCount int      `json:"tool_count"`
		Tools     []string `json:"tools"`
		Error     string   `json:"error"`
	}

	t.Run("reachable MCP server returns ok with tools", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		fakeMCP := makeFakeMCPServer(t, []string{"tool-a", "tool-b"})
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"url": fakeMCP.URL})
		resp, err := http.Post(srv.URL+"/servers/test", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers/test: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data testResult `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !envelope.Data.OK {
			t.Errorf("ok = false, want true")
		}
		if envelope.Data.ToolCount != 2 {
			t.Errorf("tool_count = %d, want 2", envelope.Data.ToolCount)
		}
		if len(envelope.Data.Tools) != 2 {
			t.Errorf("tools = %v, want [tool-a tool-b]", envelope.Data.Tools)
		}
		if envelope.Data.Error != "" {
			t.Errorf("error = %q, want empty", envelope.Data.Error)
		}
	})

	t.Run("reachable MCP server with zero tools", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		fakeMCP := makeFakeMCPServer(t, []string{})
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"url": fakeMCP.URL})
		resp, err := http.Post(srv.URL+"/servers/test", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers/test: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data testResult `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !envelope.Data.OK {
			t.Errorf("ok = false, want true")
		}
		if envelope.Data.ToolCount != 0 {
			t.Errorf("tool_count = %d, want 0", envelope.Data.ToolCount)
		}
	})

	t.Run("unreachable server returns ok=false with error", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		// Start and immediately close so the URL is valid but the server is gone.
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadURL := dead.URL
		dead.Close()

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"url": deadURL})
		resp, err := http.Post(srv.URL+"/servers/test", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers/test: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data testResult `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.OK {
			t.Errorf("ok = true, want false for unreachable server")
		}
		if envelope.Data.Error == "" {
			t.Errorf("error must be non-empty for unreachable server")
		}
		// The raw Go error chain must not be surfaced to the user.
		for _, fragment := range []string{"post tools/list:", "ensure session", "http do"} {
			if strings.Contains(envelope.Data.Error, fragment) {
				t.Errorf("error %q must not contain raw Go chain fragment %q", envelope.Data.Error, fragment)
			}
		}
		// The message should be human-readable.
		lowerErr := strings.ToLower(envelope.Data.Error)
		if !strings.Contains(lowerErr, "connection refused") && !strings.Contains(lowerErr, "could not reach server") {
			t.Errorf("error %q should mention connection refused or could not reach server", envelope.Data.Error)
		}
	})

	t.Run("context deadline produces friendly message", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping: blocks for 5s waiting for context deadline")
		}

		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		// done is closed just before the test server shuts down, unblocking any
		// in-flight handler so httptest.Server.Close() does not stall.
		done := make(chan struct{})

		// This handler never sends a response, forcing the 5-second deadline inside
		// TestConnection to expire. It blocks until done is closed.
		blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-done
		}))
		// Cleanup order: t.Cleanup calls in LIFO order — close(done) must be
		// registered AFTER blocking.Close so it runs BEFORE it.
		t.Cleanup(blocking.Close)
		t.Cleanup(func() { close(done) })

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"url": blocking.URL})
		resp, err := http.Post(srv.URL+"/servers/test", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers/test: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data testResult `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.OK {
			t.Errorf("ok = true, want false on deadline")
		}
		if !strings.Contains(envelope.Data.Error, "Connection timed out") {
			t.Errorf("error = %q, want to contain %q", envelope.Data.Error, "Connection timed out")
		}
		if strings.Contains(envelope.Data.Error, "context deadline exceeded") {
			t.Errorf("error %q must not contain raw Go text %q", envelope.Data.Error, "context deadline exceeded")
		}
	})

	t.Run("missing url returns 400", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{})
		resp, err := http.Post(srv.URL+"/servers/test", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers/test: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("invalid url scheme returns 400", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"url": "ftp://bad-scheme"})
		resp, err := http.Post(srv.URL+"/servers/test", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers/test: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}

func TestMCPToolListHandler(t *testing.T) {
	t.Run("list tools for server with no tools", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "empty-server", "http://localhost:9999")

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/servers/" + serverID + "/tools")
		if err != nil {
			t.Fatalf("GET /servers/%s/tools: %v", serverID, err)
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

	t.Run("list tools returns all fields", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")
		insertTestMCPTool(t, store, serverID, "tool-alpha")
		insertTestMCPTool(t, store, serverID, "tool-beta")

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/servers/" + serverID + "/tools")
		if err != nil {
			t.Fatalf("GET /servers/%s/tools: %v", serverID, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data []struct {
				ID          string          `json:"id"`
				ServerID    string          `json:"server_id"`
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"input_schema"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 2 {
			t.Fatalf("len(data) = %d, want 2", len(envelope.Data))
		}
		// Results are ordered by name ASC.
		if envelope.Data[0].Name != "tool-alpha" {
			t.Errorf("data[0].name = %q, want tool-alpha", envelope.Data[0].Name)
		}
		if envelope.Data[0].ServerID != serverID {
			t.Errorf("data[0].server_id = %q, want %q", envelope.Data[0].ServerID, serverID)
		}
		// input_schema must be a JSON object, not a double-encoded string.
		var schema map[string]any
		if err := json.Unmarshal(envelope.Data[0].InputSchema, &schema); err != nil {
			t.Errorf("input_schema is not a JSON object: %v (raw: %s)", err, envelope.Data[0].InputSchema)
		}
	})

	t.Run("non-existent server returns 404", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/servers/does-not-exist/tools")
		if err != nil {
			t.Fatalf("GET /servers/does-not-exist/tools: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})
}

// testEncKey returns a deterministic 32-byte key for handler tests.
func testEncKey(t *testing.T) []byte {
	t.Helper()
	k, err := admin.ParseEncryptionKey("aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd")
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}
	return k
}

// insertTestMCPServerWithHeaders inserts an MCP server row with encrypted auth headers.
func insertTestMCPServerWithHeaders(t *testing.T, s *db.Store, name, url string, encKey []byte, headers []map[string]string) string {
	t.Helper()
	id := model.NewULID()

	var ciphertext *string
	if len(headers) > 0 {
		authHeaders := make([]mcp.AuthHeader, len(headers))
		for i, h := range headers {
			authHeaders[i] = mcp.AuthHeader{Name: h["key"], Value: h["value"]}
		}
		raw, err := mcp.MarshalAuthHeaders(authHeaders)
		if err != nil {
			t.Fatalf("marshal auth headers: %v", err)
		}
		ct, err := admin.Encrypt(encKey, string(raw))
		if err != nil {
			t.Fatalf("encrypt auth headers: %v", err)
		}
		ciphertext = &ct
	}

	_, err := s.CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:                   id,
		Name:                 name,
		Url:                  url,
		CreatedAt:            "2024-01-01T00:00:00Z",
		AuthHeadersEncrypted: ciphertext,
	})
	if err != nil {
		t.Fatalf("insertTestMCPServerWithHeaders %s: %v", name, err)
	}
	return id
}

func TestMCPAuthHeaders_Create(t *testing.T) {
	t.Run("create with auth headers persists encrypted ciphertext", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		fakeMCP := makeFakeMCPServer(t, []string{})
		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]any{
			"name": "auth-server",
			"url":  fakeMCP.URL,
			"auth_headers": []map[string]string{
				{"key": "x-api-key", "value": "sk-test-secret"},
			},
		})
		resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}

		// Verify DB has encrypted ciphertext.
		rows, err := store.ListMCPServers(context.Background())
		if err != nil {
			t.Fatalf("list servers: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("want 1 server, got %d", len(rows))
		}
		if rows[0].AuthHeadersEncrypted == nil {
			t.Fatal("auth_headers_encrypted is nil, want non-nil")
		}
		// Decrypt and verify value.
		plaintext, err := admin.Decrypt(encKey, *rows[0].AuthHeadersEncrypted)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		headers, err := mcp.UnmarshalAuthHeaders([]byte(plaintext))
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(headers) != 1 || headers[0].Name != "x-api-key" || headers[0].Value != "sk-test-secret" {
			t.Errorf("headers = %+v, want [{x-api-key sk-test-secret}]", headers)
		}
	})

	t.Run("create with invalid header name returns 400", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]any{
			"name": "bad-server",
			"url":  "http://localhost:9999",
			"auth_headers": []map[string]string{
				{"key": "X-Bad\r\nInjected", "value": "v"},
			},
		})
		resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("create with reserved header name returns 400", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]any{
			"name": "reserved-server",
			"url":  "http://localhost:9999",
			"auth_headers": []map[string]string{
				{"key": "Mcp-Session-Id", "value": "inject"},
			},
		})
		resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("create with auth headers and no enc key returns 503", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		// No encryption key — pass nil.
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]any{
			"name": "nokey-server",
			"url":  "http://localhost:9999",
			"auth_headers": []map[string]string{
				{"key": "x-api-key", "value": "v"},
			},
		})
		resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", resp.StatusCode)
		}
	})
}

func TestMCPAuthHeaders_List(t *testing.T) {
	t.Run("list returns auth_header_keys without values", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())

		insertTestMCPServerWithHeaders(t, store, "keyed-server", "http://localhost:9999", encKey, []map[string]string{
			{"key": "x-api-key", "value": "SECRET"},
			{"key": "Authorization", "value": "Bearer TOKEN"},
		})

		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/servers")
		if err != nil {
			t.Fatalf("GET /servers: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				AuthHeaderKeys []string `json:"auth_header_keys"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("len(data) = %d, want 1", len(envelope.Data))
		}
		keys := envelope.Data[0].AuthHeaderKeys
		if len(keys) != 2 {
			t.Fatalf("auth_header_keys = %v, want 2 keys", keys)
		}
		// Keys should be sorted.
		if keys[0] != "Authorization" || keys[1] != "x-api-key" {
			t.Errorf("keys = %v, want [Authorization x-api-key]", keys)
		}

		// Response body must NOT contain the word "SECRET" or "TOKEN".
		raw, _ := json.Marshal(envelope.Data)
		if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "TOKEN") {
			t.Errorf("response contains plaintext secret value: %s", raw)
		}
	})
}

func TestMCPAuthHeaders_Update(t *testing.T) {
	t.Run("update changes name and url without touching auth headers", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())

		id := insertTestMCPServerWithHeaders(t, store, "upd-server", "http://localhost:9999", encKey, []map[string]string{
			{"key": "x-api-key", "value": "ORIGINAL-SECRET"},
		})

		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		// PUT with only name + url — auth_headers_encrypted must be preserved unchanged.
		body, _ := json.Marshal(map[string]any{
			"name": "upd-server-renamed",
			"url":  "http://localhost:8888",
		})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/servers/"+id, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT /servers/%s: %v", id, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		// Verify the response does NOT include auth header values and name was updated.
		var envelope struct {
			Data struct {
				Name           string   `json:"name"`
				AuthHeaderKeys []string `json:"auth_header_keys"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.Name != "upd-server-renamed" {
			t.Errorf("name = %q, want upd-server-renamed", envelope.Data.Name)
		}
		// Auth header key is still visible in response.
		if len(envelope.Data.AuthHeaderKeys) != 1 || envelope.Data.AuthHeaderKeys[0] != "x-api-key" {
			t.Errorf("auth_header_keys = %v, want [x-api-key]", envelope.Data.AuthHeaderKeys)
		}

		// Re-fetch from DB — encrypted value must still be ORIGINAL-SECRET.
		row, err := store.GetMCPServer(context.Background(), id)
		if err != nil {
			t.Fatalf("get server: %v", err)
		}
		if row.AuthHeadersEncrypted == nil {
			t.Fatal("auth_headers_encrypted is nil after update — should have been preserved")
		}
		plaintext, err := admin.Decrypt(encKey, *row.AuthHeadersEncrypted)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		headers, err := mcp.UnmarshalAuthHeaders([]byte(plaintext))
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(headers) != 1 || headers[0].Value != "ORIGINAL-SECRET" {
			t.Errorf("after name/url update, value = %q, want ORIGINAL-SECRET", headers[0].Value)
		}
	})
}

func TestSetAuthHeader(t *testing.T) {
	t.Run("adds new header to server with no existing headers", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		id := insertTestMCPServer(t, store, "bare-server", "http://localhost:9999")

		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"value": "sk-new"})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/servers/"+id+"/headers/x-api-key", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT /servers/%s/headers/x-api-key: %v", id, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		row, err := store.GetMCPServer(context.Background(), id)
		if err != nil {
			t.Fatalf("get server: %v", err)
		}
		if row.AuthHeadersEncrypted == nil {
			t.Fatal("auth_headers_encrypted is nil after set")
		}
		plaintext, _ := admin.Decrypt(encKey, *row.AuthHeadersEncrypted)
		headers, _ := mcp.UnmarshalAuthHeaders([]byte(plaintext))
		if len(headers) != 1 || headers[0].Name != "x-api-key" || headers[0].Value != "sk-new" {
			t.Errorf("headers = %+v, want [{x-api-key sk-new}]", headers)
		}
	})

	t.Run("replaces existing header case-insensitively and adopts submitted casing", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		// Server has "x-api-key" stored.
		id := insertTestMCPServerWithHeaders(t, store, "cased-server", "http://localhost:9999", encKey, []map[string]string{
			{"key": "x-api-key", "value": "OLD-VALUE"},
		})

		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		// PUT to /headers/X-Api-Key (different casing).
		body, _ := json.Marshal(map[string]string{"value": "NEW-VALUE"})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/servers/"+id+"/headers/X-Api-Key", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT /servers/%s/headers/X-Api-Key: %v", id, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		row, err := store.GetMCPServer(context.Background(), id)
		if err != nil {
			t.Fatalf("get server: %v", err)
		}
		plaintext, _ := admin.Decrypt(encKey, *row.AuthHeadersEncrypted)
		headers, _ := mcp.UnmarshalAuthHeaders([]byte(plaintext))
		// Must have exactly one header — no duplicates.
		if len(headers) != 1 {
			t.Fatalf("len(headers) = %d, want 1; headers = %+v", len(headers), headers)
		}
		// Casing must match the submitted name.
		if headers[0].Name != "X-Api-Key" {
			t.Errorf("name = %q, want X-Api-Key", headers[0].Name)
		}
		if headers[0].Value != "NEW-VALUE" {
			t.Errorf("value = %q, want NEW-VALUE", headers[0].Value)
		}
	})

	t.Run("rejects reserved header name with 400", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		id := insertTestMCPServer(t, store, "res-server", "http://localhost:9999")

		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"value": "v"})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/servers/"+id+"/headers/Mcp-Session-Id", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("rejects CRLF in header name with 400", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		id := insertTestMCPServer(t, store, "crlf-server", "http://localhost:9999")

		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"value": "v"})
		// URL-encode the CRLF to get it through the HTTP layer.
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/servers/"+id+"/headers/X-Bad%0D%0AHeader", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		defer resp.Body.Close()
		// Go's net/http will reject the CRLF at the transport layer (400 from client)
		// or the handler will return 400. Either way we must not get a 2xx.
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("status = 200, want non-200 for CRLF injection")
		}
	})

	t.Run("returns 503 when encryption key is missing", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		id := insertTestMCPServer(t, store, "nokey-server", "http://localhost:9999")

		// No encryption key.
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"value": "v"})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/servers/"+id+"/headers/x-api-key", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", resp.StatusCode)
		}
	})
}

func TestDeleteAuthHeader(t *testing.T) {
	t.Run("removes existing header", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		id := insertTestMCPServerWithHeaders(t, store, "del-server", "http://localhost:9999", encKey, []map[string]string{
			{"key": "x-api-key", "value": "SECRET"},
			{"key": "x-keep", "value": "KEEP"},
		})

		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/servers/"+id+"/headers/x-api-key", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		row, err := store.GetMCPServer(context.Background(), id)
		if err != nil {
			t.Fatalf("get server: %v", err)
		}
		if row.AuthHeadersEncrypted == nil {
			t.Fatal("auth_headers_encrypted is nil — x-keep should still be stored")
		}
		plaintext, _ := admin.Decrypt(encKey, *row.AuthHeadersEncrypted)
		headers, _ := mcp.UnmarshalAuthHeaders([]byte(plaintext))
		if len(headers) != 1 || headers[0].Name != "x-keep" {
			t.Errorf("headers = %+v, want [{x-keep KEEP}]", headers)
		}
	})

	t.Run("no-op when header is absent returns 200 with current state", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		id := insertTestMCPServerWithHeaders(t, store, "noop-server", "http://localhost:9999", encKey, []map[string]string{
			{"key": "x-existing", "value": "VALUE"},
		})

		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		// Delete a header that does not exist.
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/servers/"+id+"/headers/x-nonexistent", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		// Existing header must still be stored.
		row, err := store.GetMCPServer(context.Background(), id)
		if err != nil {
			t.Fatalf("get server: %v", err)
		}
		if row.AuthHeadersEncrypted == nil {
			t.Fatal("auth_headers_encrypted became nil after no-op delete")
		}
	})

	t.Run("deleting the last header sets column to NULL", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		encKey := testEncKey(t)
		registry := mcp.NewRegistry(store.Queries())
		id := insertTestMCPServerWithHeaders(t, store, "last-server", "http://localhost:9999", encKey, []map[string]string{
			{"key": "x-only", "value": "ONLY"},
		})

		srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/servers/"+id+"/headers/x-only", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		row, err := store.GetMCPServer(context.Background(), id)
		if err != nil {
			t.Fatalf("get server: %v", err)
		}
		if row.AuthHeadersEncrypted != nil {
			t.Error("auth_headers_encrypted is non-nil after deleting the last header — want NULL")
		}

		// Response must also show no keys.
		var envelope struct {
			Data struct {
				AuthHeaderKeys []string `json:"auth_header_keys"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data.AuthHeaderKeys) != 0 {
			t.Errorf("auth_header_keys = %v, want empty", envelope.Data.AuthHeaderKeys)
		}
	})
}

func TestMCPAuthHeaders_TestConnection(t *testing.T) {
	t.Run("test connection with auth headers passes them to MCP server", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		// MCP server that captures the auth header value.
		var gotAPIKey string
		fakeMCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
			method, _ := req["method"].(string)
			switch method {
			case "initialize":
				w.Header().Set("Mcp-Session-Id", "test-s")
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}}) //nolint:errcheck
			case "notifications/initialized":
				w.WriteHeader(http.StatusOK)
			default:
				gotAPIKey = r.Header.Get("X-Api-Key")
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}}) //nolint:errcheck
			}
		}))
		t.Cleanup(fakeMCP.Close)

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]any{
			"url": fakeMCP.URL,
			"auth_headers": []map[string]string{
				{"key": "X-Api-Key", "value": "test-key-123"},
			},
		})
		resp, err := http.Post(srv.URL+"/servers/test", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /servers/test: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		if gotAPIKey != "test-key-123" {
			t.Errorf("X-Api-Key on test request = %q, want %q", gotAPIKey, "test-key-123")
		}
	})
}

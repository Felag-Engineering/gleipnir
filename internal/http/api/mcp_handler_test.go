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

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/api"
	"github.com/rapp992/gleipnir/internal/http/auth"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// newMCPRouter wires a chi router with the MCP handler, mirroring how
// NewRouter mounts the routes in production.
func newMCPRouter(store *db.Store, registry *mcp.Registry) http.Handler {
	r := chi.NewRouter()
	h := api.NewMCPHandler(store, registry)
	r.Get("/servers", h.List)
	r.Post("/servers", h.Create)
	// /servers/test must be registered before /servers/{id} so chi does not
	// capture "test" as an id parameter.
	r.Post("/servers/test", h.TestConnection)
	r.Delete("/servers/{id}", h.Delete)
	r.Post("/servers/{id}/discover", h.Discover)
	r.Get("/servers/{id}/tools", h.ListTools)
	r.Put("/servers/{id}/tools/{toolID}/enabled", h.SetToolEnabled)
	return r
}

// withAdminUser returns a request copy with an admin UserContext injected so
// the in-handler role gate for include_disabled can be exercised in tests.
func withAdminUser(r *http.Request) *http.Request {
	ctx := auth.WithUserContext(r.Context(), "u1", "admin", []string{string(model.RoleAdmin)})
	return r.WithContext(ctx)
}

// withAuditorUser returns a request copy with an auditor UserContext injected.
func withAuditorUser(r *http.Request) *http.Request {
	ctx := auth.WithUserContext(r.Context(), "u2", "auditor", []string{string(model.RoleAuditor)})
	return r.WithContext(ctx)
}

// withOperatorUser returns a request copy with an operator UserContext injected.
func withOperatorUser(r *http.Request) *http.Request {
	ctx := auth.WithUserContext(r.Context(), "u3", "operator", []string{string(model.RoleOperator)})
	return r.WithContext(ctx)
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

	t.Run("list tools omits disabled by default", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")
		enabledID := insertTestMCPTool(t, store, serverID, "enabled-tool")
		disabledID := insertTestMCPTool(t, store, serverID, "disabled-tool")

		// Disable the second tool.
		if err := store.SetMCPToolEnabled(context.Background(), db.SetMCPToolEnabledParams{
			ID:      disabledID,
			Enabled: 0,
		}); err != nil {
			t.Fatalf("SetMCPToolEnabled: %v", err)
		}
		_ = enabledID

		h := api.NewMCPHandler(store, registry)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = setChiURLParams(req, "id", serverID)
		w := httptest.NewRecorder()
		h.ListTools(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var envelope struct {
			Data []struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("len(data) = %d, want 1 (disabled tool should be hidden)", len(envelope.Data))
		}
		if envelope.Data[0].Name != "enabled-tool" {
			t.Errorf("data[0].name = %q, want enabled-tool", envelope.Data[0].Name)
		}
		if !envelope.Data[0].Enabled {
			t.Errorf("enabled-tool should have enabled=true in response")
		}
	})

	t.Run("include_disabled returns all for admin", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")
		insertTestMCPTool(t, store, serverID, "enabled-tool")
		disabledID := insertTestMCPTool(t, store, serverID, "disabled-tool")

		if err := store.SetMCPToolEnabled(context.Background(), db.SetMCPToolEnabledParams{
			ID:      disabledID,
			Enabled: 0,
		}); err != nil {
			t.Fatalf("SetMCPToolEnabled: %v", err)
		}

		h := api.NewMCPHandler(store, registry)
		req := httptest.NewRequest(http.MethodGet, "/?include_disabled=true", nil)
		req = setChiURLParams(req, "id", serverID)
		req = withAdminUser(req)
		w := httptest.NewRecorder()
		h.ListTools(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var envelope struct {
			Data []struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 2 {
			t.Fatalf("len(data) = %d, want 2 (both tools)", len(envelope.Data))
		}
		// Results are ordered by name ASC: disabled-tool < enabled-tool.
		if envelope.Data[0].Name != "disabled-tool" {
			t.Errorf("data[0].name = %q, want disabled-tool", envelope.Data[0].Name)
		}
		if envelope.Data[0].Enabled {
			t.Errorf("disabled-tool should have enabled=false in response")
		}
		if !envelope.Data[1].Enabled {
			t.Errorf("enabled-tool should have enabled=true in response")
		}
	})

	t.Run("include_disabled silently ignored for auditor", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")
		insertTestMCPTool(t, store, serverID, "enabled-tool")
		disabledID := insertTestMCPTool(t, store, serverID, "disabled-tool")

		if err := store.SetMCPToolEnabled(context.Background(), db.SetMCPToolEnabledParams{
			ID:      disabledID,
			Enabled: 0,
		}); err != nil {
			t.Fatalf("SetMCPToolEnabled: %v", err)
		}

		h := api.NewMCPHandler(store, registry)
		req := httptest.NewRequest(http.MethodGet, "/?include_disabled=true", nil)
		req = setChiURLParams(req, "id", serverID)
		req = withAuditorUser(req)
		w := httptest.NewRecorder()
		h.ListTools(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var envelope struct {
			Data []struct {
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		// Auditors get the default enabled-only list even with include_disabled=true.
		if len(envelope.Data) != 1 {
			t.Fatalf("len(data) = %d, want 1 (disabled tool invisible to auditor)", len(envelope.Data))
		}
		if envelope.Data[0].Name != "enabled-tool" {
			t.Errorf("data[0].name = %q, want enabled-tool", envelope.Data[0].Name)
		}
	})
}

// setChiURLParams injects chi URL params into the request context so the handler
// can read them via chi.URLParam. Used in direct handler tests that bypass the
// chi router. Pairs must be provided as alternating key, value strings.
func setChiURLParams(r *http.Request, keyVals ...string) *http.Request {
	rctx := chi.NewRouteContext()
	for i := 0; i+1 < len(keyVals); i += 2 {
		rctx.URLParams.Add(keyVals[i], keyVals[i+1])
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestMCPSetToolEnabledHandler(t *testing.T) {
	t.Run("disable then re-enable", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")
		toolID := insertTestMCPTool(t, store, serverID, "my-tool")

		h := api.NewMCPHandler(store, registry)

		// Disable the tool.
		body, _ := json.Marshal(map[string]bool{"enabled": false})
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = setChiURLParams(req, "id", serverID, "toolID", toolID)
		w := httptest.NewRecorder()
		h.SetToolEnabled(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("disable: status = %d, want 200", w.Code)
		}
		var envelope struct {
			Data struct {
				Enabled bool `json:"enabled"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode disable response: %v", err)
		}
		if envelope.Data.Enabled {
			t.Error("after disable: enabled = true, want false")
		}

		// Re-enable the tool.
		body, _ = json.Marshal(map[string]bool{"enabled": true})
		req = httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = setChiURLParams(req, "id", serverID, "toolID", toolID)
		w = httptest.NewRecorder()
		h.SetToolEnabled(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("re-enable: status = %d, want 200", w.Code)
		}
		if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode re-enable response: %v", err)
		}
		if !envelope.Data.Enabled {
			t.Error("after re-enable: enabled = false, want true")
		}
	})

	t.Run("404 when server missing", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		h := api.NewMCPHandler(store, registry)
		body, _ := json.Marshal(map[string]bool{"enabled": false})
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = setChiURLParams(req, "id", "unknown-server", "toolID", "any-tool")
		w := httptest.NewRecorder()
		h.SetToolEnabled(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})

	t.Run("404 when tool missing", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")

		h := api.NewMCPHandler(store, registry)
		body, _ := json.Marshal(map[string]bool{"enabled": false})
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = setChiURLParams(req, "id", serverID, "toolID", "unknown-tool")
		w := httptest.NewRecorder()
		h.SetToolEnabled(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})

	t.Run("400 when tool belongs to different server", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverA := insertTestMCPServer(t, store, "server-a", "http://localhost:9991")
		serverB := insertTestMCPServer(t, store, "server-b", "http://localhost:9992")
		toolOnB := insertTestMCPTool(t, store, serverB, "tool-b")

		h := api.NewMCPHandler(store, registry)
		body, _ := json.Marshal(map[string]bool{"enabled": false})
		// Use server A's ID but tool B's ID.
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = setChiURLParams(req, "id", serverA, "toolID", toolOnB)
		w := httptest.NewRecorder()
		h.SetToolEnabled(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	t.Run("400 on malformed body", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")
		toolID := insertTestMCPTool(t, store, serverID, "my-tool")

		h := api.NewMCPHandler(store, registry)
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader("not json"))
		req.Header.Set("Content-Type", "application/json")
		req = setChiURLParams(req, "id", serverID, "toolID", toolID)
		w := httptest.NewRecorder()
		h.SetToolEnabled(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
}

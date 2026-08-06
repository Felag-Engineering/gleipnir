package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/schemanorm"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// fakeFeatureLister is a test double for api.SchemaFeatureLister.
type fakeFeatureLister struct {
	features map[string]llm.SchemaFeatureSet
}

func (f fakeFeatureLister) SchemaFeaturesByProvider() map[string]llm.SchemaFeatureSet {
	return f.features
}

// googleShapedFeatures mirrors the Google wire's declared SchemaFeatureSet
// (internal/llm/google/client.go): oneOf/anyOf/const are eliminable by the
// shared pass and declared unsupported; everything else is supported.
func googleShapedFeatures() llm.SchemaFeatureSet {
	return llm.SchemaFeatureSet{
		OneOf:   false,
		AnyOf:   false,
		Const:   false,
		AllOf:   true,
		Not:     true,
		Defs:    true,
		Formats: true,
	}
}

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
	r.Put("/servers/{id}/tools/{toolID}/enabled", h.SetToolEnabled)
	return r
}

// newMCPRouterWithArbiter wires a chi router with an MCPHandler that has a
// cross-source namespace arbiter. Used for tests that exercise namespace
// conflict enforcement.
func newMCPRouterWithArbiter(store *db.Store, registry *mcp.Registry, arbiter *toolregistry.Registry) http.Handler {
	r := chi.NewRouter()
	h := api.NewMCPHandler(store, registry, nil, api.WithToolNamespaceArbiter(arbiter))
	r.Get("/servers", h.List)
	r.Post("/servers", h.Create)
	r.Post("/servers/test", h.TestConnection)
	r.Delete("/servers/{id}", h.Delete)
	r.Put("/servers/{id}", h.Update)
	r.Get("/servers/{id}/tools", h.ListTools)
	r.Post("/servers/{id}/discover", h.Discover)
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

// insertTestMCPToolWithSchema inserts an MCP tool row with a caller-supplied
// input_schema and canonical_schema — a sibling of insertTestMCPTool rather
// than a signature change, since insertTestMCPTool has 19 existing call
// sites with no schema parameter. canonicalSchema may be nil to leave the
// column NULL (the pre-#738 / normalization-failure state).
func insertTestMCPToolWithSchema(t *testing.T, s *db.Store, serverID, name, inputSchema string, canonicalSchema *string) string {
	t.Helper()
	id := model.NewULID()
	_, err := s.UpsertMCPTool(context.Background(), db.UpsertMCPToolParams{
		ID:              id,
		ServerID:        serverID,
		Name:            name,
		Description:     name + " description",
		InputSchema:     inputSchema,
		CanonicalSchema: canonicalSchema,
		CreatedAt:       "2024-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("insertTestMCPToolWithSchema %s: %v", name, err)
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

	t.Run("list surfaces protocol_version, explicit null when unpinned", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		ctx := context.Background()

		pinnedID := insertTestMCPServer(t, store, "pinned", "http://localhost:9999")
		v := mcp.ProtocolVersion20260728
		if err := store.UpdateMCPServerProtocolVersion(ctx, db.UpdateMCPServerProtocolVersionParams{
			ProtocolVersion: &v, ID: pinnedID,
		}); err != nil {
			t.Fatalf("UpdateMCPServerProtocolVersion: %v", err)
		}
		insertTestMCPServer(t, store, "unpinned", "http://localhost:9998")

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/servers")
		if err != nil {
			t.Fatalf("GET /servers: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []map[string]json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 2 {
			t.Fatalf("len(data) = %d, want 2", len(envelope.Data))
		}

		for _, row := range envelope.Data {
			raw, ok := row["protocol_version"]
			if !ok {
				t.Fatalf("row %s missing protocol_version key", row["id"])
			}
			var id string
			if err := json.Unmarshal(row["id"], &id); err != nil {
				t.Fatalf("unmarshal id: %v", err)
			}
			switch id {
			case pinnedID:
				if string(raw) != `"2026-07-28"` {
					t.Errorf("pinned protocol_version = %s, want %q", raw, "2026-07-28")
				}
			default:
				if string(raw) != "null" {
					t.Errorf("unpinned protocol_version = %s, want null", raw)
				}
			}
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

	t.Run("create persists canonical_schema alongside the raw discovered schema", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		fakeMCP := makeFakeMCPServer(t, []string{"tool-a"})
		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"name": "canonical-server", "url": fakeMCP.URL})
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
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		tools, err := store.ListMCPToolsByServer(context.Background(), envelope.Data.ID)
		if err != nil {
			t.Fatalf("ListMCPToolsByServer: %v", err)
		}
		if len(tools) != 1 {
			t.Fatalf("len(tools) = %d, want 1", len(tools))
		}

		wantCanonical, err := schemanorm.Normalize(json.RawMessage(`{"type":"object"}`))
		if err != nil {
			t.Fatalf("schemanorm.Normalize: %v", err)
		}
		if tools[0].CanonicalSchema == nil {
			t.Fatal("CanonicalSchema is nil, want the normalized schema from the Create/ProbeTools write path")
		}
		if *tools[0].CanonicalSchema != string(wantCanonical) {
			t.Errorf("CanonicalSchema = %q, want %q", *tools[0].CanonicalSchema, wantCanonical)
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

	t.Run("create pins the modern protocol version", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		fakeMCP := httptest.NewServer(mcp.NewFakeMCPServer(mcp.WithFakeMode(mcp.FakeModern)))
		t.Cleanup(fakeMCP.Close)

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"name": "modern-server", "url": fakeMCP.URL})
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
				ID              string  `json:"id"`
				ProtocolVersion *string `json:"protocol_version"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.ProtocolVersion == nil || *envelope.Data.ProtocolVersion != "2026-07-28" {
			t.Errorf("body protocol_version = %v, want %q", envelope.Data.ProtocolVersion, "2026-07-28")
		}

		got, err := store.GetMCPServer(context.Background(), envelope.Data.ID)
		if err != nil {
			t.Fatalf("GetMCPServer: %v", err)
		}
		if got.ProtocolVersion == nil || *got.ProtocolVersion != "2026-07-28" {
			t.Errorf("ProtocolVersion = %v, want %q", got.ProtocolVersion, "2026-07-28")
		}
	})

	t.Run("create with zero tools still returns the pinned protocol version", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		fakeMCP := httptest.NewServer(mcp.NewFakeMCPServer(mcp.WithFakeMode(mcp.FakeModern), mcp.WithFakeTools()))
		t.Cleanup(fakeMCP.Close)

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"name": "zero-tools-server", "url": fakeMCP.URL})
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
				ProtocolVersion *string `json:"protocol_version"`
				DiscoveryError  *string `json:"discovery_error"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.ProtocolVersion == nil || *envelope.Data.ProtocolVersion != "2026-07-28" {
			t.Errorf("protocol_version = %v, want %q", envelope.Data.ProtocolVersion, "2026-07-28")
		}
		if envelope.Data.DiscoveryError != nil {
			t.Errorf("discovery_error = %v, want nil", *envelope.Data.DiscoveryError)
		}
	})

	t.Run("create pins the legacy protocol version", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		fakeMCP := httptest.NewServer(mcp.NewFakeMCPServer(mcp.WithFakeMode(mcp.FakeLegacy)))
		t.Cleanup(fakeMCP.Close)

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"name": "legacy-server", "url": fakeMCP.URL})
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
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		got, err := store.GetMCPServer(context.Background(), envelope.Data.ID)
		if err != nil {
			t.Fatalf("GetMCPServer: %v", err)
		}
		if got.ProtocolVersion == nil || *got.ProtocolVersion != "2024-11-05" {
			t.Errorf("ProtocolVersion = %v, want %q", got.ProtocolVersion, "2024-11-05")
		}
	})

	t.Run("create with unreachable server leaves protocol_version NULL", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		// Start and immediately close so URL is valid but unreachable. This is
		// also the cross-package proof that the fake is genuinely reusable —
		// the two subtests above drive mcp.NewFakeMCPServer entirely through
		// its exported surface from package api_test.
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadURL := dead.URL
		dead.Close()

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		body, _ := json.Marshal(map[string]string{"name": "unreachable-protocol-server", "url": deadURL})
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
				ID              string  `json:"id"`
				DiscoveryError  *string `json:"discovery_error"`
				ProtocolVersion *string `json:"protocol_version"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.DiscoveryError == nil {
			t.Error("expected discovery_error to be set, got nil")
		}
		if envelope.Data.ProtocolVersion != nil {
			t.Errorf("body protocol_version = %v, want nil", *envelope.Data.ProtocolVersion)
		}

		got, err := store.GetMCPServer(context.Background(), envelope.Data.ID)
		if err != nil {
			t.Fatalf("GetMCPServer: %v", err)
		}
		if got.ProtocolVersion != nil {
			t.Errorf("ProtocolVersion = %v, want nil", *got.ProtocolVersion)
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

	t.Run("delete server referenced by policy with force=true returns 204", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		id := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")

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

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/servers/"+id+"?force=true", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /servers/%s?force=true: %v", id, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}

		// Server row must actually be gone.
		if _, err := store.GetMCPServer(context.Background(), id); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("GetMCPServer after force delete: err = %v, want sql.ErrNoRows", err)
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
				Added           []string `json:"added"`
				Removed         []string `json:"removed"`
				Modified        []string `json:"modified"`
				ServedFromCache bool     `json:"served_from_cache"`
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
		// makeFakeMCPServer has no server/discover surface, so this server
		// classifies as legacy and never caches — served_from_cache must
		// always be false for it.
		if envelope.Data.ServedFromCache {
			t.Error("served_from_cache = true, want false — makeFakeMCPServer is legacy-classified and never caches")
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

	t.Run("second discover within TTL is served from cache", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())

		fakeMCP := httptest.NewServer(mcp.NewFakeMCPServer(
			mcp.WithFakeMode(mcp.FakeModern),
			mcp.WithFakeRejectLegacyHandshake(),
			mcp.WithFakeToolsListCacheHint(30000, "public"),
		))
		t.Cleanup(fakeMCP.Close)
		id := insertTestMCPServer(t, store, "cache-hint-server", fakeMCP.URL)

		srv := httptest.NewServer(newMCPRouter(store, registry))
		t.Cleanup(srv.Close)

		discover := func() bool {
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
					ServedFromCache bool `json:"served_from_cache"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			return envelope.Data.ServedFromCache
		}

		if servedFromCache := discover(); servedFromCache {
			t.Error("first discover: served_from_cache = true, want false (a real fetch)")
		}
		if servedFromCache := discover(); !servedFromCache {
			t.Error("second discover within TTL: served_from_cache = false, want true (a cache hit)")
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

		h := api.NewMCPHandler(store, registry, nil)
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

		h := api.NewMCPHandler(store, registry, nil)
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

	t.Run("include_disabled returns all for auditor", func(t *testing.T) {
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

		h := api.NewMCPHandler(store, registry, nil)
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
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		// Auditors can now see all tools when include_disabled=true is passed.
		// Results are ordered by name ASC: disabled-tool < enabled-tool.
		if len(envelope.Data) != 2 {
			t.Fatalf("len(data) = %d, want 2 (both tools)", len(envelope.Data))
		}
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

		h := api.NewMCPHandler(store, registry, nil)

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

		h := api.NewMCPHandler(store, registry, nil)
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

		h := api.NewMCPHandler(store, registry, nil)
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

		h := api.NewMCPHandler(store, registry, nil)
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

		h := api.NewMCPHandler(store, registry, nil)
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

// testEncKey returns a deterministic 32-byte key for handler tests.
func testEncKey(t *testing.T) []byte {
	t.Helper()
	k, err := crypto.ParseEncryptionKey("aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd")
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
		ct, err := crypto.Encrypt(encKey, string(raw))
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
		plaintext, err := crypto.Decrypt(encKey, *rows[0].AuthHeadersEncrypted)
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
		plaintext, err := crypto.Decrypt(encKey, *row.AuthHeadersEncrypted)
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
		plaintext, _ := crypto.Decrypt(encKey, *row.AuthHeadersEncrypted)
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
		plaintext, _ := crypto.Decrypt(encKey, *row.AuthHeadersEncrypted)
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
		plaintext, _ := crypto.Decrypt(encKey, *row.AuthHeadersEncrypted)
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

// TestListTools_SourceField verifies that every tool row in the ListTools
// response carries a "source" field formatted as "mcp:<serverName>".
func TestListTools_SourceField(t *testing.T) {
	store := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(store.Queries())
	serverID := insertTestMCPServer(t, store, "my-server", "http://localhost:9999")
	insertTestMCPTool(t, store, serverID, "tool-alpha")

	h := api.NewMCPHandler(store, registry, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = setChiURLParams(req, "id", serverID)
	w := httptest.NewRecorder()
	h.ListTools(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var envelope struct {
		Data []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(envelope.Data))
	}
	if envelope.Data[0].Source != "mcp:my-server" {
		t.Errorf("source = %q, want mcp:my-server", envelope.Data[0].Source)
	}
}

// TestCreate_Returns409_OnPluginNamespaceConflict verifies that POST /servers
// returns 409 when the probe discovers a tool whose dot-name is already owned
// by a plugin, and no mcp_servers row is created.
func TestCreate_Returns409_OnPluginNamespaceConflict(t *testing.T) {
	store := testutil.NewTestStore(t)
	arbiter := toolregistry.New()
	registry := mcp.NewRegistry(store.Queries(), mcp.WithToolNamespaceArbiter(arbiter))

	// Pre-claim "test-server.tool-a" with a plugin source.
	pluginSrc := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "test-server"}
	if err := arbiter.Reserve(toolregistry.DotName("test-server", "tool-a"), pluginSrc); err != nil {
		t.Fatalf("pre-reserve: %v", err)
	}

	fakeMCP := makeFakeMCPServer(t, []string{"tool-a"})
	srv := httptest.NewServer(newMCPRouterWithArbiter(store, registry, arbiter))
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"name": "test-server", "url": fakeMCP.URL})
	resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /servers: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}

	// No mcp_servers row must have been created.
	rawDB := store.DB()
	var count int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM mcp_servers`).Scan(&count); err != nil {
		t.Fatalf("count mcp_servers: %v", err)
	}
	if count != 0 {
		t.Errorf("mcp_servers count = %d after 409, want 0", count)
	}
}

// TestCreate_DiscoveryError_PreservedAndNoReservation verifies that when the
// pre-flight probe fails (unreachable URL), the server is still created with
// discovery_error populated and the arbiter holds no reservations for it.
func TestCreate_DiscoveryError_PreservedAndNoReservation(t *testing.T) {
	store := testutil.NewTestStore(t)
	arbiter := toolregistry.New()
	registry := mcp.NewRegistry(store.Queries(), mcp.WithToolNamespaceArbiter(arbiter))

	// Start and immediately close so URL is valid but unreachable.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	srv := httptest.NewServer(newMCPRouterWithArbiter(store, registry, arbiter))
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
			DiscoveryError *string `json:"discovery_error"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.DiscoveryError == nil {
		t.Error("discovery_error must be set when probe fails")
	}

	// Arbiter must have no reservations for this server.
	snap := arbiter.Snapshot()
	for dotName, owner := range snap {
		if owner == (toolregistry.Source{Kind: toolregistry.KindMCP, Name: "unreachable-server"}) {
			t.Errorf("arbiter has unexpected reservation %q after probe failure", dotName)
		}
	}
}

// TestCreate_RollbackOnDBFailure verifies that when the mcp_servers INSERT
// fails (duplicate name), the arbiter reservations are released so the namespace
// is not permanently locked.
func TestCreate_RollbackOnDBFailure(t *testing.T) {
	store := testutil.NewTestStore(t)
	arbiter := toolregistry.New()
	registry := mcp.NewRegistry(store.Queries(), mcp.WithToolNamespaceArbiter(arbiter))

	fakeMCP := makeFakeMCPServer(t, []string{"tool-a"})

	// Insert the server first so a duplicate will fail.
	insertTestMCPServer(t, store, "dup-server", "http://localhost:9999")

	srv := httptest.NewServer(newMCPRouterWithArbiter(store, registry, arbiter))
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"name": "dup-server", "url": fakeMCP.URL})
	resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /servers: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}

	// Arbiter must be empty for this server after the rollback.
	snap := arbiter.Snapshot()
	for dotName, owner := range snap {
		if owner == (toolregistry.Source{Kind: toolregistry.KindMCP, Name: "dup-server"}) {
			t.Errorf("arbiter has stale reservation %q after DB failure rollback", dotName)
		}
	}
}

// updateMCPServer issues PUT /servers/{id} with the given name/url and returns
// the response. Caller closes the body.
func updateMCPServer(t *testing.T, base, id, name, url string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "url": url})
	req, err := http.NewRequest(http.MethodPut, base+"/servers/"+id, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /servers/%s: %v", id, err)
	}
	return resp
}

// TestUpdate_RenameRefreshesArbiter exercises the #578 fix: a rename releases
// the arbiter reservations held under the old server name and re-reserves the
// server's tools under the new name. A rename whose new name collides with an
// existing reservation is rejected with 409 and leaves no partial state; a PUT
// that does not change the name leaves the arbiter untouched.
func TestUpdate_RenameRefreshesArbiter(t *testing.T) {
	mcpSrc := func(name string) toolregistry.Source {
		return toolregistry.Source{Kind: toolregistry.KindMCP, Name: name}
	}

	t.Run("rename releases old and re-reserves new", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		arbiter := toolregistry.New()
		registry := mcp.NewRegistry(store.Queries(), mcp.WithToolNamespaceArbiter(arbiter))

		serverID := insertTestMCPServer(t, store, "old-name", "http://localhost:9999")
		insertTestMCPTool(t, store, serverID, "tool-a")
		insertTestMCPTool(t, store, serverID, "tool-b")
		// Seed the arbiter as discovery would have: old-name owns both tools.
		if err := arbiter.ReserveBulk([]toolregistry.Reservation{
			{DotName: toolregistry.DotName("old-name", "tool-a"), Owner: mcpSrc("old-name")},
			{DotName: toolregistry.DotName("old-name", "tool-b"), Owner: mcpSrc("old-name")},
		}); err != nil {
			t.Fatalf("seed arbiter: %v", err)
		}

		srv := httptest.NewServer(newMCPRouterWithArbiter(store, registry, arbiter))
		t.Cleanup(srv.Close)

		resp := updateMCPServer(t, srv.URL, serverID, "new-name", "http://localhost:9999")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		snap := arbiter.Snapshot()
		// Old-name reservations must be gone.
		for dotName, owner := range snap {
			if owner == mcpSrc("old-name") {
				t.Errorf("stale old-name reservation %q survives rename", dotName)
			}
		}
		// New-name reservations must exist for both tools.
		for _, tool := range []string{"tool-a", "tool-b"} {
			dn := toolregistry.DotName("new-name", tool)
			if owner, ok := snap[dn]; !ok || owner != mcpSrc("new-name") {
				t.Errorf("missing new-name reservation %q (got %v, ok=%v)", dn, owner, ok)
			}
		}
	})

	t.Run("rename colliding with existing reservation is rejected without leaking state", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		arbiter := toolregistry.New()
		registry := mcp.NewRegistry(store.Queries(), mcp.WithToolNamespaceArbiter(arbiter))

		serverID := insertTestMCPServer(t, store, "old-name", "http://localhost:9999")
		insertTestMCPTool(t, store, serverID, "tool-a")
		if err := arbiter.Reserve(toolregistry.DotName("old-name", "tool-a"), mcpSrc("old-name")); err != nil {
			t.Fatalf("seed arbiter: %v", err)
		}
		// A plugin already owns "taken.tool-a" — renaming our server to "taken"
		// would collide on that dot-name.
		if err := arbiter.Reserve(toolregistry.DotName("taken", "tool-a"),
			toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "taken"}); err != nil {
			t.Fatalf("seed conflicting reservation: %v", err)
		}

		srv := httptest.NewServer(newMCPRouterWithArbiter(store, registry, arbiter))
		t.Cleanup(srv.Close)

		resp := updateMCPServer(t, srv.URL, serverID, "taken", "http://localhost:9999")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}

		// DB name must NOT have changed (no partial apply).
		row, err := store.GetMCPServer(context.Background(), serverID)
		if err != nil {
			t.Fatalf("GetMCPServer: %v", err)
		}
		if row.Name != "old-name" {
			t.Errorf("server name = %q after rejected rename, want old-name", row.Name)
		}

		// Arbiter: old-name reservation intact, no orphaned new-name slot.
		snap := arbiter.Snapshot()
		if owner, ok := snap[toolregistry.DotName("old-name", "tool-a")]; !ok || owner != mcpSrc("old-name") {
			t.Errorf("old-name reservation lost after rejected rename (got %v, ok=%v)", owner, ok)
		}
		if owner, ok := snap[toolregistry.DotName("taken", "tool-a")]; !ok ||
			owner != (toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "taken"}) {
			t.Errorf("pre-existing conflicting reservation mutated (got %v, ok=%v)", owner, ok)
		}
	})

	t.Run("rename to a name held by another MCP server is rejected without corrupting its reservations", func(t *testing.T) {
		// Regression for the corruption path where two MCP servers share tool
		// names: renaming server B to server A's name must not delete A's
		// arbiter slots, even though the dot-names would look idempotent to the
		// arbiter. The DB-name pre-check rejects the rename before reservation.
		store := testutil.NewTestStore(t)
		arbiter := toolregistry.New()
		registry := mcp.NewRegistry(store.Queries(), mcp.WithToolNamespaceArbiter(arbiter))

		// Server A: name "taken", tool "tool-a" — owns "taken.tool-a".
		serverA := insertTestMCPServer(t, store, "taken", "http://localhost:9999")
		insertTestMCPTool(t, store, serverA, "tool-a")
		if err := arbiter.Reserve(toolregistry.DotName("taken", "tool-a"), mcpSrc("taken")); err != nil {
			t.Fatalf("seed server A reservation: %v", err)
		}

		// Server B: name "old", same tool "tool-a" — owns "old.tool-a".
		serverB := insertTestMCPServer(t, store, "old", "http://localhost:8888")
		insertTestMCPTool(t, store, serverB, "tool-a")
		if err := arbiter.Reserve(toolregistry.DotName("old", "tool-a"), mcpSrc("old")); err != nil {
			t.Fatalf("seed server B reservation: %v", err)
		}

		srv := httptest.NewServer(newMCPRouterWithArbiter(store, registry, arbiter))
		t.Cleanup(srv.Close)

		resp := updateMCPServer(t, srv.URL, serverB, "taken", "http://localhost:8888")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}

		// Server A's reservation must survive untouched.
		snap := arbiter.Snapshot()
		if owner, ok := snap[toolregistry.DotName("taken", "tool-a")]; !ok || owner != mcpSrc("taken") {
			t.Errorf("server A reservation corrupted by rejected rename (got %v, ok=%v)", owner, ok)
		}
		// Server B keeps its old-name reservation (rename did not commit).
		if owner, ok := snap[toolregistry.DotName("old", "tool-a")]; !ok || owner != mcpSrc("old") {
			t.Errorf("server B old-name reservation lost after rejected rename (got %v, ok=%v)", owner, ok)
		}
		// Server B's DB name must be unchanged.
		row, err := store.GetMCPServer(context.Background(), serverB)
		if err != nil {
			t.Fatalf("GetMCPServer: %v", err)
		}
		if row.Name != "old" {
			t.Errorf("server B name = %q after rejected rename, want old", row.Name)
		}
	})

	t.Run("no-op when name is unchanged", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		arbiter := toolregistry.New()
		registry := mcp.NewRegistry(store.Queries(), mcp.WithToolNamespaceArbiter(arbiter))

		serverID := insertTestMCPServer(t, store, "stable-name", "http://localhost:9999")
		insertTestMCPTool(t, store, serverID, "tool-a")
		if err := arbiter.Reserve(toolregistry.DotName("stable-name", "tool-a"), mcpSrc("stable-name")); err != nil {
			t.Fatalf("seed arbiter: %v", err)
		}
		before := arbiter.Snapshot()

		srv := httptest.NewServer(newMCPRouterWithArbiter(store, registry, arbiter))
		t.Cleanup(srv.Close)

		// Same name, different URL.
		resp := updateMCPServer(t, srv.URL, serverID, "stable-name", "http://localhost:8888")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		after := arbiter.Snapshot()
		if len(after) != len(before) {
			t.Fatalf("arbiter size changed on no-op rename: before=%d after=%d", len(before), len(after))
		}
		for dn, owner := range before {
			if after[dn] != owner {
				t.Errorf("arbiter reservation %q changed on no-op rename: %v -> %v", dn, owner, after[dn])
			}
		}
	})
}

// TestSimplifiedForProviders drives api.SimplifiedForProviders (exported via
// export_test.go) — the DTO-mapping helper deciding which configured
// providers get listed against a tool. This is the DoD's "DTO mapping" test
// on the Go side.
func TestSimplifiedForProviders(t *testing.T) {
	// oneOfFalse and constFalse are two DISTINCT SchemaFeatureSets, each
	// restricting exactly one of the two constructs the test schema below
	// uses, so a schema can be lossy under either independently.
	oneOfFalse := llm.SchemaFeatureSet{OneOf: false, AnyOf: true, AllOf: true, Not: true, Defs: true, Const: true, Formats: true}
	constFalse := llm.SchemaFeatureSet{OneOf: true, AnyOf: true, AllOf: true, Not: true, Defs: true, Const: false, Formats: true}
	defsFalse := llm.SchemaFeatureSet{OneOf: true, AnyOf: true, AllOf: true, Not: true, Defs: false, Const: true, Formats: true}

	oversizedSchema := json.RawMessage(`{"type":"object","description":"` + strings.Repeat("a", 1<<20) + `"}`)

	// thresholdExceedingSchema is otherwise lossy under oneOfFalse (it has a
	// top-level "oneOf") but sits well under TranslateForFeatures' own 1 MiB
	// input cap, so a [] result here can only come from
	// api.SimplifiedForProviders' own maxSimplificationHintBytes gate, not
	// from ErrSchemaLimitExceeded.
	thresholdExceedingSchema := json.RawMessage(
		`{"oneOf":[{"type":"string"},{"type":"integer"}],"description":"` + strings.Repeat("a", 100*1024) + `"}`)

	tests := []struct {
		name       string
		schema     json.RawMessage
		restricted map[llm.SchemaFeatureSet][]string
		want       []string
	}{
		{
			name:       "empty restricted map",
			schema:     json.RawMessage(`{"oneOf":[{"type":"string"},{"type":"integer"}]}`),
			restricted: map[llm.SchemaFeatureSet][]string{},
			want:       []string{},
		},
		{
			name:       "empty schema",
			schema:     json.RawMessage(``),
			restricted: map[llm.SchemaFeatureSet][]string{oneOfFalse: {"google"}},
			want:       []string{},
		},
		{
			name:       "restricted set, schema with no oneOf/const passes through byte-identical",
			schema:     json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
			restricted: map[llm.SchemaFeatureSet][]string{oneOfFalse: {"google"}},
			want:       []string{},
		},
		{
			name:       "restricted set OneOf:false, schema with oneOf is lossy",
			schema:     json.RawMessage(`{"oneOf":[{"type":"string"},{"type":"integer"}]}`),
			restricted: map[llm.SchemaFeatureSet][]string{oneOfFalse: {"google"}},
			want:       []string{"google"},
		},
		{
			name:       "restricted set Const:false, schema with const is lossy",
			schema:     json.RawMessage(`{"const":"fixed"}`),
			restricted: map[llm.SchemaFeatureSet][]string{constFalse: {"google"}},
			want:       []string{"google"},
		},
		{
			name:   "two distinct restricted sets, both lossy, sorted",
			schema: json.RawMessage(`{"oneOf":[{"type":"string"},{"type":"integer"}],"const":"fixed"}`),
			restricted: map[llm.SchemaFeatureSet][]string{
				oneOfFalse: {"google"},
				constFalse: {"other"},
			},
			want: []string{"google", "other"},
		},
		{
			name:       "one restricted set shared by two provider names, both returned sorted",
			schema:     json.RawMessage(`{"oneOf":[{"type":"string"},{"type":"integer"}]}`),
			restricted: map[llm.SchemaFeatureSet][]string{oneOfFalse: {"zeta", "alpha"}},
			want:       []string{"alpha", "zeta"},
		},
		{
			name:       "unrewritable feature ($ref with Defs:false) omits the provider rather than erroring",
			schema:     json.RawMessage(`{"$ref":"#/foo"}`),
			restricted: map[llm.SchemaFeatureSet][]string{defsFalse: {"google"}},
			want:       []string{},
		},
		{
			name:       "schema exceeding TranslateForFeatures' own 1 MiB input limit omits the provider",
			schema:     oversizedSchema,
			restricted: map[llm.SchemaFeatureSet][]string{oneOfFalse: {"google"}},
			want:       []string{},
		},
		{
			name:       "schema exceeding the per-tool size threshold omits the provider without erroring",
			schema:     thresholdExceedingSchema,
			restricted: map[llm.SchemaFeatureSet][]string{oneOfFalse: {"google"}},
			want:       []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := api.SimplifiedForProviders("srv-1", "my-tool", tt.schema, tt.restricted)
			if got == nil {
				t.Fatal("result is nil, want non-nil ([] must render as [] in JSON, not null)")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRestrictedFeatureSets drives api.RestrictedFeatureSets (exported via
// export_test.go).
func TestRestrictedFeatureSets(t *testing.T) {
	t.Run("empty input yields empty map", func(t *testing.T) {
		got := api.RestrictedFeatureSets(map[string]llm.SchemaFeatureSet{})
		if len(got) != 0 {
			t.Fatalf("got %v, want empty map", got)
		}
	})

	t.Run("full-support providers are dropped", func(t *testing.T) {
		got := api.RestrictedFeatureSets(map[string]llm.SchemaFeatureSet{
			"anthropic": llm.FullSchemaSupport(),
			"openai":    llm.FullSchemaSupport(),
		})
		if len(got) != 0 {
			t.Fatalf("got %v, want empty map (every provider declares full support)", got)
		}
	})

	t.Run("restricted providers are grouped by identical SchemaFeatureSet", func(t *testing.T) {
		restricted := googleShapedFeatures()
		got := api.RestrictedFeatureSets(map[string]llm.SchemaFeatureSet{
			"anthropic": llm.FullSchemaSupport(),
			"google":    restricted,
			"other":     restricted,
		})
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1 distinct restricted set", len(got))
		}
		names := got[restricted]
		sort.Strings(names)
		want := []string{"google", "other"}
		if !reflect.DeepEqual(names, want) {
			t.Errorf("got %v, want %v", names, want)
		}
	})
}

// TestListTools_SimplifiedFor exercises simplified_for at the HTTP handler
// level, following TestListTools_SourceField's shape.
func TestListTools_SimplifiedFor(t *testing.T) {
	oneOfSchema := `{"oneOf":[{"type":"string"},{"type":"integer"}]}`
	plainSchema := `{"type":"object"}`

	listTools := func(t *testing.T, store *db.Store, registry *mcp.Registry, serverID string, opts ...api.MCPHandlerOption) map[string][]string {
		t.Helper()
		h := api.NewMCPHandler(store, registry, nil, opts...)
		req := httptest.NewRequest(http.MethodGet, "/?include_disabled=true", nil)
		req = setChiURLParams(req, "id", serverID)
		w := httptest.NewRecorder()
		h.ListTools(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		var envelope struct {
			Data []struct {
				Name          string   `json:"name"`
				SimplifiedFor []string `json:"simplified_for"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		out := make(map[string][]string, len(envelope.Data))
		for _, tool := range envelope.Data {
			out[tool.Name] = tool.SimplifiedFor
		}
		return out
	}

	t.Run("handler built WITHOUT WithSchemaFeatures: every row has []", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "srv", "http://localhost:9999")
		insertTestMCPToolWithSchema(t, store, serverID, "oneof-tool", oneOfSchema, &oneOfSchema)

		got := listTools(t, store, registry, serverID)
		if len(got["oneof-tool"]) != 0 {
			t.Errorf("simplified_for = %v, want empty (no feature lister configured)", got["oneof-tool"])
		}
	})

	t.Run("handler built WITH a lister declaring only full-support wires: every row has []", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "srv", "http://localhost:9999")
		insertTestMCPToolWithSchema(t, store, serverID, "oneof-tool", oneOfSchema, &oneOfSchema)

		lister := fakeFeatureLister{features: map[string]llm.SchemaFeatureSet{"anthropic": llm.FullSchemaSupport()}}
		got := listTools(t, store, registry, serverID, api.WithSchemaFeatures(lister))
		if len(got["oneof-tool"]) != 0 {
			t.Errorf("simplified_for = %v, want empty (every configured wire is full-support)", got["oneof-tool"])
		}
	})

	t.Run("handler built WITH a Google-shaped restricted set", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "srv", "http://localhost:9999")
		insertTestMCPToolWithSchema(t, store, serverID, "oneof-tool", oneOfSchema, &oneOfSchema)
		insertTestMCPToolWithSchema(t, store, serverID, "plain-tool", plainSchema, &plainSchema)

		lister := fakeFeatureLister{features: map[string]llm.SchemaFeatureSet{"google": googleShapedFeatures()}}
		got := listTools(t, store, registry, serverID, api.WithSchemaFeatures(lister))
		if want := []string{"google"}; !reflect.DeepEqual(got["oneof-tool"], want) {
			t.Errorf("oneof-tool simplified_for = %v, want %v", got["oneof-tool"], want)
		}
		if len(got["plain-tool"]) != 0 {
			t.Errorf("plain-tool simplified_for = %v, want empty", got["plain-tool"])
		}
	})

	t.Run("NULL canonical_schema yields no chip (no fallback to raw input_schema)", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "srv", "http://localhost:9999")
		insertTestMCPToolWithSchema(t, store, serverID, "oneof-tool", oneOfSchema, nil)

		lister := fakeFeatureLister{features: map[string]llm.SchemaFeatureSet{"google": googleShapedFeatures()}}
		got := listTools(t, store, registry, serverID, api.WithSchemaFeatures(lister))
		if len(got["oneof-tool"]) != 0 {
			t.Errorf("simplified_for = %v, want empty (NULL canonical_schema must not fall back to raw "+
				"input_schema — the chip must shadow #744's exact enforcement, which also degrades to "+
				"key-presence validation for this row)", got["oneof-tool"])
		}
	})

	t.Run("non-nil but empty canonical_schema yields no chip", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "srv", "http://localhost:9999")
		empty := "   " // whitespace-only: exercises the TrimSpace branch, not just nil
		insertTestMCPToolWithSchema(t, store, serverID, "oneof-tool", oneOfSchema, &empty)

		lister := fakeFeatureLister{features: map[string]llm.SchemaFeatureSet{"google": googleShapedFeatures()}}
		got := listTools(t, store, registry, serverID, api.WithSchemaFeatures(lister))
		if len(got["oneof-tool"]) != 0 {
			t.Errorf("simplified_for = %v, want empty (non-nil-but-empty canonical_schema must not fall "+
				"back to raw input_schema)", got["oneof-tool"])
		}
	})

	t.Run("a tool whose canonical schema exceeds the size threshold does not 500 the list; every other tool is still present", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "srv", "http://localhost:9999")
		oversized := `{"type":"object","description":"` + strings.Repeat("a", 1<<20) + `"}`
		insertTestMCPToolWithSchema(t, store, serverID, "oversized-tool", oversized, &oversized)
		insertTestMCPToolWithSchema(t, store, serverID, "plain-tool", plainSchema, &plainSchema)

		lister := fakeFeatureLister{features: map[string]llm.SchemaFeatureSet{"google": googleShapedFeatures()}}
		got := listTools(t, store, registry, serverID, api.WithSchemaFeatures(lister))
		if _, ok := got["oversized-tool"]; !ok {
			t.Fatal("oversized-tool missing from the list; a single bad schema must not drop it")
		}
		if len(got["oversized-tool"]) != 0 {
			t.Errorf("oversized-tool simplified_for = %v, want empty (exceeds the per-tool size threshold, "+
				"skipped before translate runs)", got["oversized-tool"])
		}
		if _, ok := got["plain-tool"]; !ok {
			t.Error("plain-tool missing from the list after a sibling tool's schema was skipped")
		}
	})

	t.Run("a tool whose schema fails translation (not size) does not 500 the list; every other tool is still present", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		registry := mcp.NewRegistry(store.Queries())
		serverID := insertTestMCPServer(t, store, "srv", "http://localhost:9999")
		refSchema := `{"$ref":"#/foo"}`
		insertTestMCPToolWithSchema(t, store, serverID, "ref-tool", refSchema, &refSchema)
		insertTestMCPToolWithSchema(t, store, serverID, "plain-tool", plainSchema, &plainSchema)

		// Defs:false makes $ref unrewritable (ErrUnsupportedSchemaFeature),
		// unlike googleShapedFeatures above (Defs: true) — this exercises the
		// translate-error path specifically, distinct from the size-threshold
		// skip in the sub-test above.
		unsupportedRefs := llm.SchemaFeatureSet{OneOf: true, AnyOf: true, AllOf: true, Not: true, Defs: false, Const: true, Formats: true}
		lister := fakeFeatureLister{features: map[string]llm.SchemaFeatureSet{"google": unsupportedRefs}}
		got := listTools(t, store, registry, serverID, api.WithSchemaFeatures(lister))
		if _, ok := got["ref-tool"]; !ok {
			t.Fatal("ref-tool missing from the list; a single bad schema must not drop it")
		}
		if len(got["ref-tool"]) != 0 {
			t.Errorf("ref-tool simplified_for = %v, want empty (translation failed, omitted)", got["ref-tool"])
		}
		if _, ok := got["plain-tool"]; !ok {
			t.Error("plain-tool missing from the list after a sibling tool's schema errored")
		}
	})
}

// TestSetToolEnabled_ReturnsSimplifiedFor verifies the mutation response
// carries simplified_for, matching ListTools' shape — the frontend replaces
// the cached row from this response, so omitting the field here would make
// the chip vanish on toggle.
func TestSetToolEnabled_ReturnsSimplifiedFor(t *testing.T) {
	store := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(store.Queries())
	serverID := insertTestMCPServer(t, store, "srv", "http://localhost:9999")
	oneOfSchema := `{"oneOf":[{"type":"string"},{"type":"integer"}]}`
	toolID := insertTestMCPToolWithSchema(t, store, serverID, "oneof-tool", oneOfSchema, &oneOfSchema)

	lister := fakeFeatureLister{features: map[string]llm.SchemaFeatureSet{"google": googleShapedFeatures()}}
	h := api.NewMCPHandler(store, registry, nil, api.WithSchemaFeatures(lister))

	body, _ := json.Marshal(map[string]bool{"enabled": false})
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = setChiURLParams(req, "id", serverID, "toolID", toolID)
	w := httptest.NewRecorder()
	h.SetToolEnabled(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			SimplifiedFor []string `json:"simplified_for"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if want := []string{"google"}; !reflect.DeepEqual(envelope.Data.SimplifiedFor, want) {
		t.Errorf("simplified_for = %v, want %v", envelope.Data.SimplifiedFor, want)
	}
}

// --- managed endpoints (#819) -----------------------------------------------

// seedManagedServer inserts a plugin, an instance, and a managed mcp_servers
// row pointing at it.
func seedManagedServer(t *testing.T, store *db.Store) (serverID, instanceID string) {
	t.Helper()
	if _, err := store.DB().Exec(
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES ('pl1', 'slack', '1.0.0', '{}', 'pk', 'active', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin: %v", err)
	}
	if _, err := store.DB().Exec(
		`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, subscription_scope_json, handshake_versions, health_state, version, created_at, updated_at)
		 VALUES ('inst-1', 'pl1', 'slack-main', '{}', '{}', '{}', 'healthy', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin instance: %v", err)
	}
	if _, err := store.DB().Exec(
		`INSERT INTO mcp_servers(id, name, url, created_at, plugin_instance_id, protocol_version)
		 VALUES ('srv-managed', 'slack-main', 'http://10.83.0.2:8080/mcp', '2024-01-01T00:00:00Z', 'inst-1', '2026-07-28')`,
	); err != nil {
		t.Fatalf("insert managed server: %v", err)
	}
	return "srv-managed", "inst-1"
}

// A managed entry appears in the ordinary server list — it is one MCP client
// stack, not a parallel one — but it is labelled and marked non-editable so the
// UI does not offer an edit the API will refuse.
func TestMCPHandler_ListLabelsManagedEntries(t *testing.T) {
	store := testutil.NewTestStore(t)
	seedManagedServer(t, store)
	if _, err := store.Queries().CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID: "srv-external", Name: "github", Url: "https://api.example.com/mcp", CreatedAt: "2024-01-02T00:00:00Z",
	}); err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}

	router := newMCPRouter(store, mcp.NewRegistry(store.Queries()))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/servers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data []struct {
			ID               string  `json:"id"`
			TrustTier        string  `json:"trust_tier"`
			Editable         bool    `json:"editable"`
			PluginInstanceID *string `json:"plugin_instance_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("got %d servers, want the managed and the external one", len(resp.Data))
	}

	for _, s := range resp.Data {
		switch s.ID {
		case "srv-managed":
			if s.TrustTier != "managed" || s.Editable {
				t.Errorf("managed row = %+v, want trust_tier=managed and editable=false", s)
			}
			if s.PluginInstanceID == nil || *s.PluginInstanceID != "inst-1" {
				t.Errorf("managed row does not name its instance: %+v", s)
			}
		case "srv-external":
			if s.TrustTier != "external" || !s.Editable {
				t.Errorf("external row = %+v, want trust_tier=external and editable=true", s)
			}
			if s.PluginInstanceID != nil {
				t.Errorf("external row names an instance: %+v", s)
			}
		}
	}
}

// Every operator mutation is refused. 409, not 403: an admin has every
// permission and still cannot do it, because the conflict is with what the row
// is, not with who is asking.
func TestMCPHandler_ManagedEntriesAreNotOperatorEditable(t *testing.T) {
	key := make([]byte, 32)

	tests := []struct {
		name    string
		request func() *http.Request
	}{
		{
			name: "rename or repoint",
			request: func() *http.Request {
				body := `{"name":"something-else","url":"https://elsewhere.example.com/mcp"}`
				r := httptest.NewRequest(http.MethodPut, "/servers/srv-managed", bytes.NewBufferString(body))
				r.Header.Set("Content-Type", "application/json")
				return r
			},
		},
		{
			name: "delete",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodDelete, "/servers/srv-managed", nil)
			},
		},
		{
			name: "set an auth header",
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodPut, "/servers/srv-managed/headers/X-Api-Key",
					bytes.NewBufferString(`{"value":"secret"}`))
				r.Header.Set("Content-Type", "application/json")
				return r
			},
		},
		{
			name: "delete an auth header",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodDelete, "/servers/srv-managed/headers/X-Api-Key", nil)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			seedManagedServer(t, store)
			router := newMCPRouter(store, mcp.NewRegistry(store.Queries()), key)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, withAdminUser(tc.request()))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body = %s", rec.Code, rec.Body.String())
			}

			// The row is untouched — a refusal that had already half-applied
			// would be worse than one that succeeded.
			after, err := store.Queries().GetMCPServer(context.Background(), "srv-managed")
			if err != nil {
				t.Fatalf("the managed row is gone after a refused %s: %v", tc.name, err)
			}
			if after.Name != "slack-main" || after.Url != "http://10.83.0.2:8080/mcp" {
				t.Errorf("the refused mutation still landed: %+v", after)
			}
			if after.AuthHeadersEncrypted != nil {
				t.Error("a header was written to a managed endpoint")
			}
		})
	}
}

// Removing the instance is how a managed entry goes away, and it takes the
// route with it — a row pointing at a deleted instance is a dangling endpoint
// the agent could still resolve a tool through.
func TestMCPHandler_ManagedEntryDisappearsWithItsInstance(t *testing.T) {
	store := testutil.NewTestStore(t)
	_, instanceID := seedManagedServer(t, store)

	if _, err := store.DB().Exec(`DELETE FROM plugin_instances WHERE id = ?`, instanceID); err != nil {
		t.Fatalf("delete instance: %v", err)
	}

	router := newMCPRouter(store, mcp.NewRegistry(store.Queries()))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/servers", nil))

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Errorf("the managed entry outlived its instance: %+v", resp.Data)
	}
}

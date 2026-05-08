package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// --- test manifest YAML constants ---

const notifyOnlyManifestYAML = `
id: test-notify-only
name: Notify Only Plugin
version: 1.0.0
services:
  channel: v1
channels:
  - implements_notify: true
    implements_request: false
    config_schema:
      type: object
      properties:
        channel: { type: string }
`

const bothCapManifestYAML = `
id: test-both-cap
name: Both Cap Plugin
version: 1.0.0
services:
  channel: v1
channels:
  - implements_notify: true
    implements_request: true
    config_schema:
      type: object
      properties:
        channel: { type: string }
`

const nonChannelManifestYAML = `
id: test-tool-only
name: Tool Only Plugin
version: 1.0.0
services:
  tool: v1
`

// --- test fixture seeding helpers ---

func seedPlugin(tb testing.TB, s *db.Store, id, manifestYAML string) string {
	tb.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.Queries().CreatePlugin(context.Background(), db.CreatePluginParams{
		ID:               id,
		Name:             id,
		PluginVersion:    "1.0.0",
		ManifestSnapshot: manifestYAML,
		TrustedPubkey:    "test-key",
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		tb.Fatalf("seedPlugin %s: %v", id, err)
	}
	return id
}

func seedPluginInstance(tb testing.TB, s *db.Store, instanceID, pluginID string) string {
	tb.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.Queries().CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID:                instanceID,
		PluginID:          pluginID,
		InstanceName:      instanceID,
		ConfigJson:        "{}",
		HandshakeVersions: "[]",
		HealthState:       "healthy",
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if err != nil {
		tb.Fatalf("seedPluginInstance %s: %v", instanceID, err)
	}
	return instanceID
}


// seedAudience creates an audience row directly via the store, returning the ID.
func seedAudience(tb testing.TB, s *db.Store, name string, disableFallback bool) string {
	tb.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	id := model.NewULID()
	flag := int64(0)
	if disableFallback {
		flag = 1
	}
	_, err := s.Queries().CreatePluginAudience(context.Background(), db.CreatePluginAudienceParams{
		ID:                   id,
		Name:                 name,
		CreatedByUserID:      nil,
		CreatedAt:            now,
		UpdatedAt:            now,
		DisableInAppFallback: flag,
	})
	if err != nil {
		tb.Fatalf("seedAudience %s: %v", name, err)
	}
	return id
}

// seedAudienceEntry inserts an entry for an existing audience.
func seedAudienceEntry(tb testing.TB, s *db.Store, audienceID, instanceID string, position int64) {
	tb.Helper()
	_, err := s.Queries().CreateAudienceEntry(context.Background(), db.CreateAudienceEntryParams{
		ID:               model.NewULID(),
		AudienceID:       audienceID,
		PluginInstanceID: instanceID,
		Position:         position,
		Notify:           1,
		Request:          0,
		ConfigJson:       "{}",
	})
	if err != nil {
		tb.Fatalf("seedAudienceEntry: %v", err)
	}
}

// newAudienceRouter wires a chi router with the AudienceHandler.
func newAudienceRouter(store *db.Store, snap *configvalidate.Snapshotter) http.Handler {
	h := api.NewAudienceHandler(store, snap, time.Now)
	r := chi.NewRouter()
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.Get)
	r.Put("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
	r.Get("/{id}/references", h.References)
	return r
}

// newAudienceRouterWithRoles mirrors the production wiring in router.go: the
// same per-route auth.RequireRole gating used by BuildRouter, so role-matrix
// tests exercise the actual middleware chain rather than only the handlers.
func newAudienceRouterWithRoles(store *db.Store, snap *configvalidate.Snapshotter) http.Handler {
	h := api.NewAudienceHandler(store, snap, time.Now)
	r := chi.NewRouter()
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor)).Get("/", h.List)
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Post("/", h.Create)
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor)).Get("/{id}", h.Get)
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Put("/{id}", h.Update)
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).Delete("/{id}", h.Delete)
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor)).Get("/{id}/references", h.References)
	return r
}

// withRole injects a user context with the given role. The user ID is left
// empty so the handler's created_by_user_id stays nil (no FK constraint).
func withRole(r *http.Request, role model.Role) *http.Request {
	ctx := auth.WithUserContext(r.Context(), "", "", []string{string(role)})
	return r.WithContext(ctx)
}

func do(t *testing.T, handler http.Handler, method, path string, body any, role model.Role) *httptest.ResponseRecorder {
	t.Helper()
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req = withRole(req, role)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func parseData(t *testing.T, w *httptest.ResponseRecorder, out any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v\nbody: %s", err, w.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			t.Fatalf("decode data: %v\ndata: %s", err, string(env.Data))
		}
	}
}

func parseError(t *testing.T, w *httptest.ResponseRecorder) (errMsg, detail string) {
	t.Helper()
	var env struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v\nbody: %s", err, w.Body.String())
	}
	return env.Error, env.Detail
}

// --- List tests ---

func TestAudienceList_Empty(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	w := do(t, newAudienceRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var items []map[string]any
	parseData(t, w, &items)
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
}

func TestAudienceList_MultipleAudiences(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id1 := seedAudience(t, store, "alpha", false)
	id2 := seedAudience(t, store, "beta", false)

	pluginID := seedPlugin(t, store, "p1", notifyOnlyManifestYAML)
	instID := seedPluginInstance(t, store, "i1", pluginID)
	seedAudienceEntry(t, store, id1, instID, 0)

	w := do(t, newAudienceRouter(store, snap), http.MethodGet, "/", nil, model.RoleAuditor)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var items []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		EntryCount int    `json:"entry_count"`
	}
	parseData(t, w, &items)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	byID := make(map[string]int)
	for _, item := range items {
		byID[item.ID] = item.EntryCount
	}
	if byID[id1] != 1 {
		t.Errorf("alpha entry_count = %d, want 1", byID[id1])
	}
	if byID[id2] != 0 {
		t.Errorf("beta entry_count = %d, want 0", byID[id2])
	}
}

// --- Get tests ---

func TestAudienceGet_NotFound(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	w := do(t, newAudienceRouter(store, snap), http.MethodGet, "/nonexistent", nil, model.RoleOperator)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestAudienceGet_WithSyntheticInAppEntry(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "test-audience", false) // disable=false → in-app entry appended

	w := do(t, newAudienceRouter(store, snap), http.MethodGet, "/"+id, nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var dto struct {
		ID      string `json:"id"`
		Entries []struct {
			ID   string `json:"id"`
			Auto bool   `json:"auto"`
		} `json:"entries"`
	}
	parseData(t, w, &dto)

	if dto.ID != id {
		t.Errorf("id = %q, want %q", dto.ID, id)
	}
	// The synthetic in-app entry should be present.
	if len(dto.Entries) != 1 {
		t.Fatalf("want 1 entry (synthetic), got %d", len(dto.Entries))
	}
	if !dto.Entries[0].Auto {
		t.Error("expected auto=true for synthetic in-app entry")
	}
}

// --- Create tests ---

func TestAudienceCreate_Happy(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	w := do(t, newAudienceRouter(store, snap), http.MethodPost, "/", map[string]any{
		"name":    "my-audience",
		"entries": []any{},
	}, model.RoleAdmin)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", w.Code, w.Body.String())
	}
	var dto struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	parseData(t, w, &dto)
	if dto.Name != "my-audience" {
		t.Errorf("name = %q, want %q", dto.Name, "my-audience")
	}
}

func TestAudienceCreate_DuplicateName(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	seedAudience(t, store, "dup-name", false)

	w := do(t, newAudienceRouter(store, snap), http.MethodPost, "/", map[string]any{
		"name":    "dup-name",
		"entries": []any{},
	}, model.RoleAdmin)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestAudienceCreate_MissingName(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	w := do(t, newAudienceRouter(store, snap), http.MethodPost, "/", map[string]any{
		"entries": []any{},
	}, model.RoleAdmin)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAudienceCreate_InvalidPluginInstance(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	w := do(t, newAudienceRouter(store, snap), http.MethodPost, "/", map[string]any{
		"name": "bad-entry-audience",
		"entries": []any{
			map[string]any{
				"plugin_instance_id": "nonexistent-instance",
				"notify":             true,
				"request":            false,
				"config":             map[string]any{},
			},
		},
	}, model.RoleAdmin)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\nbody: %s", w.Code, w.Body.String())
	}
}

func TestAudienceCreate_DisableFallbackWithNoRequestCapable(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "notify-only-plugin", notifyOnlyManifestYAML)
	instID := seedPluginInstance(t, store, "notify-only-inst", pluginID)

	w := do(t, newAudienceRouter(store, snap), http.MethodPost, "/", map[string]any{
		"name":                   "no-request-audience",
		"disable_in_app_fallback": true,
		"entries": []any{
			map[string]any{
				"plugin_instance_id": instID,
				"notify":             true,
				"request":            false, // notify-only plugin; request not set
				"config":             map[string]any{},
			},
		},
	}, model.RoleAdmin)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\nbody: %s", w.Code, w.Body.String())
	}
}

// --- Update tests ---

func TestAudienceUpdate_Happy(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "update-me", false)

	w := do(t, newAudienceRouter(store, snap), http.MethodPut, "/"+id, map[string]any{
		"name":             "updated-name",
		"entries":          []any{},
		"expected_version": 0,
	}, model.RoleAdmin)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	var dto struct {
		Name    string `json:"name"`
		Version int64  `json:"version"`
	}
	parseData(t, w, &dto)
	if dto.Name != "updated-name" {
		t.Errorf("name = %q, want updated-name", dto.Name)
	}
	if dto.Version != 1 {
		t.Errorf("version = %d, want 1 (incremented)", dto.Version)
	}
}

func TestAudienceUpdate_CASConflict(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "cas-audience", false)

	w := do(t, newAudienceRouter(store, snap), http.MethodPut, "/"+id, map[string]any{
		"name":             "new-name",
		"entries":          []any{},
		"expected_version": 99, // stale version
	}, model.RoleAdmin)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409\nbody: %s", w.Code, w.Body.String())
	}
}

func TestAudienceUpdate_MissingExpectedVersion(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "update-no-version", false)

	w := do(t, newAudienceRouter(store, snap), http.MethodPut, "/"+id, map[string]any{
		"name":    "new-name",
		"entries": []any{},
		// no expected_version
	}, model.RoleAdmin)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAudienceUpdate_NotFound(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	w := do(t, newAudienceRouter(store, snap), http.MethodPut, "/nonexistent", map[string]any{
		"name":             "new-name",
		"entries":          []any{},
		"expected_version": 0,
	}, model.RoleAdmin)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// --- Delete tests ---

func TestAudienceDelete_Happy(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "delete-me", false)

	w := do(t, newAudienceRouter(store, snap), http.MethodDelete, "/"+id, nil, model.RoleAdmin)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204\nbody: %s", w.Code, w.Body.String())
	}
}

func TestAudienceDelete_NotFound(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	w := do(t, newAudienceRouter(store, snap), http.MethodDelete, "/nonexistent", nil, model.RoleAdmin)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestAudienceDelete_ReferencedByPolicy_Returns409(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "referenced-aud", false)
	// Insert a policy that references this audience by name.
	testutil.InsertPolicy(t, store, model.NewULID(), "my-policy", "webhook",
		"name: my-policy\ntrigger:\n  type: webhook\n  auth: none\nagent:\n  model: x\n  task: t\naudience: referenced-aud\n")

	w := do(t, newAudienceRouter(store, snap), http.MethodDelete, "/"+id, nil, model.RoleAdmin)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409\nbody: %s", w.Code, w.Body.String())
	}

	_, detail := parseError(t, w)
	if detail == "" {
		t.Error("expected non-empty detail with policy names")
	}
	if detail != "my-policy" {
		t.Errorf("detail = %q, want %q", detail, "my-policy")
	}
}

// --- References tests ---

func TestAudienceReferences_Empty(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "no-refs", false)

	w := do(t, newAudienceRouter(store, snap), http.MethodGet, "/"+id+"/references", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var dto struct {
		Policies     []any `json:"policies"`
		InFlightRuns []any `json:"in_flight_runs"`
	}
	parseData(t, w, &dto)
	if len(dto.Policies) != 0 {
		t.Errorf("got %d policies, want 0", len(dto.Policies))
	}
	if len(dto.InFlightRuns) != 0 {
		t.Errorf("got %d in_flight_runs, want 0", len(dto.InFlightRuns))
	}
}

func TestAudienceReferences_WithPolicyRef(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "ref-aud", false)
	testutil.InsertPolicy(t, store, model.NewULID(), "ref-policy", "webhook",
		"name: ref-policy\ntrigger:\n  type: webhook\n  auth: none\nagent:\n  model: x\n  task: t\naudience: ref-aud\n")

	w := do(t, newAudienceRouter(store, snap), http.MethodGet, "/"+id+"/references", nil, model.RoleAuditor)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var dto struct {
		Policies []struct {
			Name string `json:"name"`
		} `json:"policies"`
	}
	parseData(t, w, &dto)
	if len(dto.Policies) != 1 {
		t.Fatalf("got %d policies, want 1", len(dto.Policies))
	}
	if dto.Policies[0].Name != "ref-policy" {
		t.Errorf("policy name = %q, want ref-policy", dto.Policies[0].Name)
	}
}

func TestAudienceReferences_NotFound(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	w := do(t, newAudienceRouter(store, snap), http.MethodGet, "/nonexistent/references", nil, model.RoleOperator)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// --- Role matrix tests ---

func TestAudienceRoleMatrix(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "role-test", false)

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		role       model.Role
		wantStatus int
	}{
		// Admin can do everything.
		{"admin GET /", http.MethodGet, "/", nil, model.RoleAdmin, http.StatusOK},
		{"admin GET /:id", http.MethodGet, "/" + id, nil, model.RoleAdmin, http.StatusOK},
		{"admin GET /:id/references", http.MethodGet, "/" + id + "/references", nil, model.RoleAdmin, http.StatusOK},
		{"admin POST /", http.MethodPost, "/", map[string]any{"name": "admin-new", "entries": []any{}}, model.RoleAdmin, http.StatusCreated},
		{"admin PUT /:id", http.MethodPut, "/" + id, map[string]any{"name": "role-test", "entries": []any{}, "expected_version": 0}, model.RoleAdmin, http.StatusOK},

		// Operator can mutate.
		{"operator GET /", http.MethodGet, "/", nil, model.RoleOperator, http.StatusOK},
		{"operator POST /", http.MethodPost, "/", map[string]any{"name": "op-new", "entries": []any{}}, model.RoleOperator, http.StatusCreated},

		// Auditor can only read.
		{"auditor GET /", http.MethodGet, "/", nil, model.RoleAuditor, http.StatusOK},
		{"auditor GET /:id", http.MethodGet, "/" + id, nil, model.RoleAuditor, http.StatusOK},
		{"auditor GET /:id/references", http.MethodGet, "/" + id + "/references", nil, model.RoleAuditor, http.StatusOK},
	}

	router := newAudienceRouter(store, snap)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, router, tc.method, tc.path, tc.body, tc.role)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d\nbody: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}

	// Role-middleware enforcement: routes wired with auth.RequireRole exactly
	// as in BuildRouter. Auditor mutations and any approver access must 403.
	gatedTests := []struct {
		name       string
		method     string
		path       string
		body       any
		role       model.Role
		wantStatus int
	}{
		{"auditor POST / forbidden", http.MethodPost, "/", map[string]any{"name": "x", "entries": []any{}}, model.RoleAuditor, http.StatusForbidden},
		{"auditor PUT /:id forbidden", http.MethodPut, "/" + id, map[string]any{"name": "role-test", "entries": []any{}, "expected_version": 0}, model.RoleAuditor, http.StatusForbidden},
		{"auditor DELETE /:id forbidden", http.MethodDelete, "/" + id, nil, model.RoleAuditor, http.StatusForbidden},
		{"approver GET / forbidden", http.MethodGet, "/", nil, model.RoleApprover, http.StatusForbidden},
		{"approver POST / forbidden", http.MethodPost, "/", map[string]any{"name": "x", "entries": []any{}}, model.RoleApprover, http.StatusForbidden},
	}
	gatedRouter := newAudienceRouterWithRoles(store, snap)
	for _, tc := range gatedTests {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, gatedRouter, tc.method, tc.path, tc.body, tc.role)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d\nbody: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

// TestAudienceCreate_WithBothCapEntry tests that creating an audience with a
// both-capable entry (notify+request) and disable_in_app_fallback=true succeeds.
func TestAudienceCreate_WithBothCapEntry_DisableFallback(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "both-cap-plugin", bothCapManifestYAML)
	instID := seedPluginInstance(t, store, "both-cap-inst", pluginID)

	w := do(t, newAudienceRouter(store, snap), http.MethodPost, "/", map[string]any{
		"name":                    "both-cap-audience",
		"disable_in_app_fallback": true,
		"entries": []any{
			map[string]any{
				"plugin_instance_id": instID,
				"notify":             true,
				"request":            true,
				"config":             map[string]any{},
			},
		},
	}, model.RoleAdmin)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", w.Code, w.Body.String())
	}
}

// TestAudienceCreate_NonChannelPlugin verifies that an entry pointing at a
// non-channel plugin is rejected with 422.
func TestAudienceCreate_NonChannelPlugin(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "tool-plugin", nonChannelManifestYAML)
	instID := seedPluginInstance(t, store, "tool-inst", pluginID)

	w := do(t, newAudienceRouter(store, snap), http.MethodPost, "/", map[string]any{
		"name": "non-channel-audience",
		"entries": []any{
			map[string]any{
				"plugin_instance_id": instID,
				"notify":             false,
				"request":            false,
				"config":             map[string]any{},
			},
		},
	}, model.RoleAdmin)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\nbody: %s", w.Code, w.Body.String())
	}
}

// TestAudienceUpdate_EntryReplace verifies that PUT replaces entries and
// returns the new entry list.
func TestAudienceUpdate_EntryReplace(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "replace-entries", false)
	pluginID := seedPlugin(t, store, "notify-plugin2", notifyOnlyManifestYAML)
	instID := seedPluginInstance(t, store, "notify-inst2", pluginID)
	seedAudienceEntry(t, store, id, instID, 0)

	// Update: remove all entries.
	w := do(t, newAudienceRouter(store, snap), http.MethodPut, "/"+id, map[string]any{
		"name":             "replace-entries",
		"entries":          []any{},
		"expected_version": 0,
	}, model.RoleAdmin)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var dto struct {
		Entries []struct {
			Auto bool `json:"auto"`
		} `json:"entries"`
	}
	parseData(t, w, &dto)

	// After clearing persisted entries, only the synthetic in-app entry remains
	// (disable=false by default).
	if len(dto.Entries) != 1 || !dto.Entries[0].Auto {
		t.Errorf("expected 1 synthetic entry, got %d entries: %+v", len(dto.Entries), dto.Entries)
	}
}

// TestAudienceList_EntryCountAndRefCount verifies bulk-query correctness.
func TestAudienceList_EntryCountAndRefCount(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	id := seedAudience(t, store, "counted", false)

	pluginID := seedPlugin(t, store, "p-count", notifyOnlyManifestYAML)
	inst1 := seedPluginInstance(t, store, "i-count-1", pluginID)
	inst2 := seedPluginInstance(t, store, "i-count-2", pluginID)
	seedAudienceEntry(t, store, id, inst1, 0)
	seedAudienceEntry(t, store, id, inst2, 1)

	// Insert a policy that references "counted" in the audience field.
	testutil.InsertPolicy(t, store, model.NewULID(), "p-ref", "webhook",
		"name: p-ref\ntrigger:\n  type: webhook\n  auth: none\nagent:\n  model: x\n  task: t\naudience: counted\n")

	w := do(t, newAudienceRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var items []struct {
		ID                      string `json:"id"`
		EntryCount              int    `json:"entry_count"`
		ReferencedByPolicyCount int    `json:"referenced_by_policy_count"`
	}
	parseData(t, w, &items)

	var found bool
	for _, item := range items {
		if item.ID == id {
			found = true
			if item.EntryCount != 2 {
				t.Errorf("entry_count = %d, want 2", item.EntryCount)
			}
			if item.ReferencedByPolicyCount != 1 {
				t.Errorf("referenced_by_policy_count = %d, want 1", item.ReferencedByPolicyCount)
			}
		}
	}
	if !found {
		t.Fatal("audience not found in list response")
	}
}

// TestAudienceCreate_HappyWithEntry tests creating an audience with a valid
// notify-only entry.
func TestAudienceCreate_HappyWithEntry(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "entry-plugin", notifyOnlyManifestYAML)
	instID := seedPluginInstance(t, store, "entry-inst", pluginID)

	w := do(t, newAudienceRouter(store, snap), http.MethodPost, "/", map[string]any{
		"name": "with-entry",
		"entries": []any{
			map[string]any{
				"plugin_instance_id": instID,
				"notify":             true,
				"request":            false,
				"config":             map[string]any{},
			},
		},
	}, model.RoleAdmin)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", w.Code, w.Body.String())
	}
}


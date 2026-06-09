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

const triggerPluginManifestYAML = `
id: test-trigger-plugin
name: Slack
version: 1.0.0
services:
  trigger: v1
event_kinds:
  - kind: channel_message
    description: A message posted in a channel
  - kind: direct_message
    description: A direct message to the bot
`

// triggerPluginWithExamplesManifestYAML is a manifest with binding_schema and
// examples, including one malformed example (missing payload) to verify skipping.
const triggerPluginWithExamplesManifestYAML = `
id: test-trigger-examples
name: SlackWithExamples
version: 1.0.0
services:
  trigger: v1
event_kinds:
  - kind: channel_message
    description: A message posted in a channel
    binding_schema:
      type: object
      properties:
        channel:
          type: string
    examples:
      - name: incident-channel
        payload:
          channel: "#incidents"
          text: "alert fired"
      - name: general-channel
        payload:
          channel: "#general"
          text: "hello"
      - name: malformed-missing-payload
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
		"name":                    "no-request-audience",
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

// --- ListPluginInstances tests ---

// newPluginInstancesRouter wires a chi router for the GET /plugin-instances endpoint.
func newPluginInstancesRouter(store *db.Store, snap *configvalidate.Snapshotter) http.Handler {
	h := api.NewAudienceHandler(store, snap, time.Now)
	r := chi.NewRouter()
	r.Get("/", h.ListPluginInstances)
	return r
}

func TestListPluginInstances_Empty(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var items []map[string]any
	parseData(t, w, &items)
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
}

func TestListPluginInstances_WithEventKinds(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "trigger-plugin", triggerPluginManifestYAML)
	instID := seedPluginInstance(t, store, "slack-prod", pluginID)

	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var items []struct {
		ID           string `json:"id"`
		PluginName   string `json:"plugin_name"`
		InstanceName string `json:"instance_name"`
		EventKinds   []struct {
			Kind        string `json:"kind"`
			Description string `json:"description"`
		} `json:"event_kinds"`
	}
	parseData(t, w, &items)

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].ID != instID {
		t.Errorf("id = %q, want %q", items[0].ID, instID)
	}
	if items[0].PluginName != "Slack" {
		t.Errorf("plugin_name = %q, want Slack", items[0].PluginName)
	}
	if items[0].InstanceName != "slack-prod" {
		t.Errorf("instance_name = %q, want slack-prod", items[0].InstanceName)
	}
	if len(items[0].EventKinds) != 2 {
		t.Fatalf("got %d event_kinds, want 2", len(items[0].EventKinds))
	}
	if items[0].EventKinds[0].Kind != "channel_message" {
		t.Errorf("event_kinds[0].kind = %q, want channel_message", items[0].EventKinds[0].Kind)
	}
	if items[0].EventKinds[1].Kind != "direct_message" {
		t.Errorf("event_kinds[1].kind = %q, want direct_message", items[0].EventKinds[1].Kind)
	}
}

func TestListPluginInstances_ChannelPluginHasNoEventKinds(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "channel-plugin2", notifyOnlyManifestYAML)
	seedPluginInstance(t, store, "notify-inst2", pluginID)

	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleAuditor)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var items []struct {
		ImplementsNotify bool  `json:"implements_notify"`
		EventKinds       []any `json:"event_kinds"`
	}
	parseData(t, w, &items)

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if !items[0].ImplementsNotify {
		t.Error("expected implements_notify=true")
	}
	if len(items[0].EventKinds) != 0 {
		t.Errorf("expected 0 event_kinds, got %d", len(items[0].EventKinds))
	}
}

func TestListPluginInstances_RoleMatrix(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	r := chi.NewRouter()
	h := api.NewAudienceHandler(store, snap, time.Now)
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor)).
		Get("/", h.ListPluginInstances)

	tests := []struct {
		role       model.Role
		wantStatus int
	}{
		{model.RoleAdmin, http.StatusOK},
		{model.RoleOperator, http.StatusOK},
		{model.RoleAuditor, http.StatusOK},
		{model.RoleApprover, http.StatusForbidden},
	}
	for _, tc := range tests {
		w := do(t, r, http.MethodGet, "/", nil, tc.role)
		if w.Code != tc.wantStatus {
			t.Errorf("role=%s: status=%d, want %d", tc.role, w.Code, tc.wantStatus)
		}
	}
}

// TestListPluginInstances_WithEventKindExamplesAndBindingSchema verifies that
// binding_schema and examples are decoded and returned, and that malformed
// examples (missing payload) are skipped without failing the request.
func TestListPluginInstances_WithEventKindExamplesAndBindingSchema(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "trigger-examples-plugin", triggerPluginWithExamplesManifestYAML)
	instID := seedPluginInstance(t, store, "slack-examples", pluginID)

	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var items []struct {
		ID         string `json:"id"`
		PluginName string `json:"plugin_name"`
		EventKinds []struct {
			Kind          string         `json:"kind"`
			BindingSchema map[string]any `json:"binding_schema"`
			Examples      []struct {
				Name    string         `json:"name"`
				Payload map[string]any `json:"payload"`
			} `json:"examples"`
		} `json:"event_kinds"`
	}
	parseData(t, w, &items)

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].ID != instID {
		t.Errorf("id = %q, want %q", items[0].ID, instID)
	}
	if items[0].PluginName != "SlackWithExamples" {
		t.Errorf("plugin_name = %q, want SlackWithExamples", items[0].PluginName)
	}
	if len(items[0].EventKinds) != 1 {
		t.Fatalf("got %d event_kinds, want 1", len(items[0].EventKinds))
	}

	ek := items[0].EventKinds[0]
	if ek.Kind != "channel_message" {
		t.Errorf("kind = %q, want channel_message", ek.Kind)
	}
	if ek.BindingSchema == nil {
		t.Error("binding_schema is nil, want non-nil")
	}

	// Two valid examples; the malformed one (missing payload) is skipped.
	if len(ek.Examples) != 2 {
		t.Fatalf("got %d examples, want 2 (malformed one skipped)", len(ek.Examples))
	}
	if ek.Examples[0].Name != "incident-channel" {
		t.Errorf("examples[0].name = %q, want incident-channel", ek.Examples[0].Name)
	}
	if ek.Examples[1].Name != "general-channel" {
		t.Errorf("examples[1].name = %q, want general-channel", ek.Examples[1].Name)
	}
	if ek.Examples[0].Payload["channel"] != "#incidents" {
		t.Errorf("examples[0].payload.channel = %v, want #incidents", ek.Examples[0].Payload["channel"])
	}
}

// toolPluginManifestYAML declares a ToolService plugin with two tools.
const toolPluginManifestYAML = `
id: test-tool-plugin
name: MyTools
version: 2.0.0
services:
  tool: v1
tools:
  - name: send_message
    description: Send a message to a channel
  - name: list_channels
    description: List available channels
`

// oauthManifestYAML declares an oauth2_authcode strategy so we can test
// that auth_strategy is populated from the manifest Auth field.
const oauthManifestYAML = `
id: test-oauth-plugin
name: OAuth Plugin
version: 1.0.0
auth:
  strategy: oauth2_authcode
`

// TestListPluginInstances_LastOAuthCallbackURL verifies that last_oauth_callback_url
// is included in the DTO when set on the instance row, and omitted when nil (#230).
func TestListPluginInstances_LastOAuthCallbackURL(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "oauth-plugin-cb", oauthManifestYAML)
	instID := seedPluginInstance(t, store, "oauth-inst-cb", pluginID)

	const wantURL = "https://gleipnir.example.com/api/v1/admin/plugins/oauth/callback"

	// Write a last_oauth_callback_url via the sqlc query.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := store.Queries().UpdatePluginInstanceOAuthCallback(context.Background(), db.UpdatePluginInstanceOAuthCallbackParams{
		LastOauthCallbackUrl: func() *string { s := wantURL; return &s }(),
		UpdatedAt:            now,
		ID:                   instID,
		ExpectedVersion:      0,
	})
	if err != nil {
		t.Fatalf("UpdatePluginInstanceOAuthCallback: %v", err)
	}

	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var items []struct {
		ID                   string `json:"id"`
		LastOauthCallbackUrl string `json:"last_oauth_callback_url"`
	}
	parseData(t, w, &items)

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].LastOauthCallbackUrl != wantURL {
		t.Errorf("last_oauth_callback_url = %q, want %q", items[0].LastOauthCallbackUrl, wantURL)
	}
}

// TestListPluginInstances_LastOAuthCallbackURL_OmittedWhenNil verifies that the
// last_oauth_callback_url field is omitted (not marshalled as "") when nil (#230).
func TestListPluginInstances_LastOAuthCallbackURL_OmittedWhenNil(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "oauth-plugin-nil", oauthManifestYAML)
	seedPluginInstance(t, store, "oauth-inst-nil", pluginID)

	// No UpdatePluginInstanceOAuthCallback call — last_oauth_callback_url stays NULL.
	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	// Decode into a raw map so we can distinguish omitted from "".
	var raw []map[string]json.RawMessage
	parseData(t, w, &raw)

	if len(raw) != 1 {
		t.Fatalf("got %d items, want 1", len(raw))
	}
	if _, present := raw[0]["last_oauth_callback_url"]; present {
		t.Error("last_oauth_callback_url should be omitted from JSON when nil, but was present")
	}
}

// TestListPluginInstances_AuthStrategyAndHealthDetail verifies that the
// auth_strategy and health_detail fields are populated in the DTO (#228).
func TestListPluginInstances_AuthStrategyAndHealthDetail(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "oauth-plugin", oauthManifestYAML)
	instID := seedPluginInstance(t, store, "oauth-inst", pluginID)

	// Drive the instance to unhealthy with a detail string.
	detail := "oauth refresh failed: token expired"
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := store.Queries().UpdatePluginInstanceHealth(context.Background(), db.UpdatePluginInstanceHealthParams{
		HealthState:     "unhealthy",
		HealthDetail:    &detail,
		UpdatedAt:       now,
		ID:              instID,
		ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("UpdatePluginInstanceHealth: %v", err)
	}

	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var items []struct {
		ID           string `json:"id"`
		AuthStrategy string `json:"auth_strategy"`
		HealthDetail string `json:"health_detail"`
	}
	parseData(t, w, &items)

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].AuthStrategy != "oauth2_authcode" {
		t.Errorf("auth_strategy = %q, want %q", items[0].AuthStrategy, "oauth2_authcode")
	}
	if items[0].HealthDetail != detail {
		t.Errorf("health_detail = %q, want %q", items[0].HealthDetail, detail)
	}
}

// TestListPluginInstances_Tools verifies that the tools field is populated from
// the manifest's tools declarations for a ToolService plugin.
func TestListPluginInstances_Tools(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "tool-plugin", toolPluginManifestYAML)
	instID := seedPluginInstance(t, store, "mytools-prod", pluginID)

	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var items []struct {
		ID    string `json:"id"`
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	parseData(t, w, &items)

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].ID != instID {
		t.Errorf("id = %q, want %q", items[0].ID, instID)
	}
	if len(items[0].Tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(items[0].Tools))
	}
	if items[0].Tools[0].Name != "send_message" {
		t.Errorf("tools[0].name = %q, want send_message", items[0].Tools[0].Name)
	}
	if items[0].Tools[0].Description != "Send a message to a channel" {
		t.Errorf("tools[0].description = %q, want %q", items[0].Tools[0].Description, "Send a message to a channel")
	}
	if items[0].Tools[1].Name != "list_channels" {
		t.Errorf("tools[1].name = %q, want list_channels", items[0].Tools[1].Name)
	}
}

// TestListPluginInstances_ToolsOmittedForNonToolPlugin verifies that the tools
// field is omitted (not present in JSON) for plugins that declare no tools.
func TestListPluginInstances_ToolsOmittedForNonToolPlugin(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	pluginID := seedPlugin(t, store, "no-tools-plugin", triggerPluginManifestYAML)
	seedPluginInstance(t, store, "notrigger-inst", pluginID)

	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var raw []map[string]json.RawMessage
	parseData(t, w, &raw)

	if len(raw) != 1 {
		t.Fatalf("got %d items, want 1", len(raw))
	}
	if _, present := raw[0]["tools"]; present {
		t.Error("tools should be omitted from JSON when no tools declared, but was present")
	}
}

// TestListPluginInstances_PluginVersionAndServices verifies that plugin_version
// and services are populated from the manifest for tool-only, trigger-only, and
// channel-only plugins.
func TestListPluginInstances_PluginVersionAndServices(t *testing.T) {
	tests := []struct {
		name         string
		manifestYAML string
		wantVersion  string
		wantServices []string
	}{
		{
			name:         "trigger plugin",
			manifestYAML: triggerPluginManifestYAML,
			wantVersion:  "1.0.0",
			wantServices: []string{"trigger"},
		},
		{
			name:         "channel plugin (both caps)",
			manifestYAML: bothCapManifestYAML,
			wantVersion:  "1.0.0",
			wantServices: []string{"channel"},
		},
		{
			name:         "tool-only plugin",
			manifestYAML: nonChannelManifestYAML,
			wantVersion:  "1.0.0",
			wantServices: []string{"tool"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			snap := configvalidate.NewSnapshotter(store.Queries())
			t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

			pluginID := seedPlugin(t, store, "test-plugin-"+tc.name, tc.manifestYAML)
			seedPluginInstance(t, store, "test-inst-"+tc.name, pluginID)

			w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleOperator)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
			}

			var items []struct {
				PluginVersion string   `json:"plugin_version"`
				Services      []string `json:"services"`
			}
			parseData(t, w, &items)

			if len(items) != 1 {
				t.Fatalf("got %d items, want 1", len(items))
			}
			if items[0].PluginVersion != tc.wantVersion {
				t.Errorf("plugin_version = %q, want %q", items[0].PluginVersion, tc.wantVersion)
			}
			if len(items[0].Services) != len(tc.wantServices) {
				t.Fatalf("services = %v, want %v", items[0].Services, tc.wantServices)
			}
			for i, svc := range tc.wantServices {
				if items[0].Services[i] != svc {
					t.Errorf("services[%d] = %q, want %q", i, items[0].Services[i], svc)
				}
			}
		})
	}
}

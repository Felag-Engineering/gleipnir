package api_test

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// --- fixture manifests ---

const bindingTestManifestYAML = `
id: binding-test-plugin
name: BindingTestPlugin
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
        pattern:
          type: string
          format: regex
        mention_only:
          type: boolean
    examples:
      - name: incident
        payload:
          channel: "#incidents"
          text: "alert"
  - kind: no_schema
    description: Event kind without a binding schema
`

// newBindingTestRouter wires a chi router with both AudienceHandler (for
// seeding the snapshotter) and BindingTestHandler.
func newBindingTestRouter(store *db.Store, snap *configvalidate.Snapshotter) http.Handler {
	bth := api.NewBindingTestHandler(snap)
	r := chi.NewRouter()
	r.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator, model.RoleAuditor)).
		Post("/{iid}/event-kinds/{kind}/test-binding", bth.Test)
	return r
}

func seedBindingTestPlugin(tb testing.TB, s *db.Store) string {
	tb.Helper()
	pluginID := seedPlugin(tb, s, "binding-test-plugin", bindingTestManifestYAML)
	return seedPluginInstance(tb, s, "binding-plugin-inst", pluginID)
}

// --- tests ---

func TestBindingTest_HappyPath_MixedResults(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	instID := seedBindingTestPlugin(t, store)

	body := map[string]any{
		"binding": map[string]any{"channel": "#incidents"},
		"payloads": []any{
			map[string]any{"channel": "#incidents", "text": "fire"},
			map[string]any{"channel": "#general", "text": "hello"},
		},
	}
	w := do(t, newBindingTestRouter(store, snap), http.MethodPost,
		"/"+instID+"/event-kinds/channel_message/test-binding", body, model.RoleOperator)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Results []struct {
			Match bool   `json:"match"`
			Error string `json:"error,omitempty"`
		} `json:"results"`
	}
	parseData(t, w, &resp)

	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	if !resp.Results[0].Match {
		t.Error("results[0].match = false, want true (#incidents matches)")
	}
	if resp.Results[1].Match {
		t.Error("results[1].match = true, want false (#general does not match)")
	}
	if resp.Results[0].Error != "" {
		t.Errorf("results[0].error = %q, want empty", resp.Results[0].Error)
	}
}

func TestBindingTest_UnknownInstance_404(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	body := map[string]any{
		"binding":  map[string]any{},
		"payloads": []any{},
	}
	w := do(t, newBindingTestRouter(store, snap), http.MethodPost,
		"/nonexistent-inst/event-kinds/channel_message/test-binding", body, model.RoleAdmin)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404\nbody: %s", w.Code, w.Body.String())
	}
}

func TestBindingTest_UnknownEventKind_404(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	instID := seedBindingTestPlugin(t, store)

	body := map[string]any{
		"binding":  map[string]any{},
		"payloads": []any{},
	}
	w := do(t, newBindingTestRouter(store, snap), http.MethodPost,
		"/"+instID+"/event-kinds/nonexistent_kind/test-binding", body, model.RoleAdmin)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404\nbody: %s", w.Code, w.Body.String())
	}
}

func TestBindingTest_EmptyPayloads_200(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	instID := seedBindingTestPlugin(t, store)

	body := map[string]any{
		"binding":  map[string]any{"channel": "#incidents"},
		"payloads": []any{},
	}
	w := do(t, newBindingTestRouter(store, snap), http.MethodPost,
		"/"+instID+"/event-kinds/channel_message/test-binding", body, model.RoleAdmin)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Results []any `json:"results"`
	}
	parseData(t, w, &resp)

	if len(resp.Results) != 0 {
		t.Errorf("got %d results, want 0", len(resp.Results))
	}
}

func TestBindingTest_InvalidRegexCompile_400(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	instID := seedBindingTestPlugin(t, store)

	body := map[string]any{
		"binding": map[string]any{
			"pattern": "[invalid(regex",
		},
		"payloads": []any{},
	}
	w := do(t, newBindingTestRouter(store, snap), http.MethodPost,
		"/"+instID+"/event-kinds/channel_message/test-binding", body, model.RoleAdmin)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\nbody: %s", w.Code, w.Body.String())
	}

	_, detail := parseError(t, w)
	if detail == "" {
		t.Error("expected non-empty detail for compile error")
	}
}

func TestBindingTest_MentionOnly_Operator(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	instID := seedBindingTestPlugin(t, store)

	body := map[string]any{
		"binding": map[string]any{"mention_only": true},
		"payloads": []any{
			map[string]any{"mentioned": true},
			map[string]any{"mentioned": false},
			map[string]any{},
		},
	}
	w := do(t, newBindingTestRouter(store, snap), http.MethodPost,
		"/"+instID+"/event-kinds/channel_message/test-binding", body, model.RoleOperator)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Results []struct {
			Match bool `json:"match"`
		} `json:"results"`
	}
	parseData(t, w, &resp)

	if len(resp.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(resp.Results))
	}
	if !resp.Results[0].Match {
		t.Error("results[0]: expected match=true when mentioned=true")
	}
	if resp.Results[1].Match {
		t.Error("results[1]: expected match=false when mentioned=false")
	}
	if resp.Results[2].Match {
		t.Error("results[2]: expected match=false when mentioned absent")
	}
}

func TestBindingTest_AuditorAllowed(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	instID := seedBindingTestPlugin(t, store)

	body := map[string]any{
		"binding":  map[string]any{},
		"payloads": []any{},
	}
	w := do(t, newBindingTestRouter(store, snap), http.MethodPost,
		"/"+instID+"/event-kinds/channel_message/test-binding", body, model.RoleAuditor)

	if w.Code != http.StatusOK {
		t.Fatalf("auditor: status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
}

func TestBindingTest_ApproverForbidden(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	instID := seedBindingTestPlugin(t, store)

	body := map[string]any{
		"binding":  map[string]any{},
		"payloads": []any{},
	}
	w := do(t, newBindingTestRouter(store, snap), http.MethodPost,
		"/"+instID+"/event-kinds/channel_message/test-binding", body, model.RoleApprover)

	if w.Code != http.StatusForbidden {
		t.Fatalf("approver: status = %d, want 403\nbody: %s", w.Code, w.Body.String())
	}
}

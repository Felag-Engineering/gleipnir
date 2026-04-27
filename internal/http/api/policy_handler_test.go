package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/api"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// newPolicyHandlerStore opens a fresh migrated store for handler tests.
// Delegates to testutil.NewTestStore — kept as an alias so the many call
// sites in this package don't all need updating at once.
func newPolicyHandlerStore(t *testing.T) *db.Store {
	t.Helper()
	return testutil.NewTestStore(t)
}

// newPolicyRouter wires a chi router with the policy handler, mirroring how
// NewRouter mounts the routes in production.
func newPolicyRouter(store *db.Store) http.Handler {
	r := chi.NewRouter()
	svc := policy.NewService(store, nil, nil, nil, nil)
	h := api.NewPolicyHandler(store, svc, nil, nil, nil)
	r.Get("/policies", h.List)
	r.Post("/policies", h.Create)
	r.Get("/policies/{id}", h.Get)
	r.Put("/policies/{id}", h.Update)
	r.Post("/policies/{id}/pause", h.Pause)
	r.Post("/policies/{id}/resume", h.Resume)
	r.Delete("/policies/{id}", h.Delete)
	return r
}

// newPolicyRouterWithLookup is like newPolicyRouter but passes a ToolLookup to
// the service so that tool-reference warnings can be exercised in tests.
func newPolicyRouterWithLookup(store *db.Store, lookup policy.ToolLookup) http.Handler {
	r := chi.NewRouter()
	svc := policy.NewService(store, lookup, nil, nil, nil)
	h := api.NewPolicyHandler(store, svc, nil, nil, nil)
	r.Get("/policies", h.List)
	r.Post("/policies", h.Create)
	r.Get("/policies/{id}", h.Get)
	r.Put("/policies/{id}", h.Update)
	r.Post("/policies/{id}/pause", h.Pause)
	r.Post("/policies/{id}/resume", h.Resume)
	r.Delete("/policies/{id}", h.Delete)
	return r
}

// alwaysMissingLookup is a ToolLookup stub that reports every tool as absent.
// Used to exercise the "unresolvable tool refs" warning path.
type alwaysMissingLookup struct{}

func (alwaysMissingLookup) ToolExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

// insertTestPolicy inserts a webhook policy row. Delegates to testutil.InsertPolicy.
func insertTestPolicy(t *testing.T, s *db.Store, id, name, yaml string) {
	t.Helper()
	testutil.InsertPolicy(t, s, id, name, "webhook", yaml)
}

// insertTestRun inserts a run row. Delegates to testutil.InsertRun.
func insertTestRun(t *testing.T, s *db.Store, id, policyID, status string) {
	t.Helper()
	testutil.InsertRun(t, s, id, policyID, model.RunStatus(status))
}

func TestPolicyListHandler(t *testing.T) {
	t.Run("no policies returns empty array not null", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
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
		// Must be "[]", not "null".
		if string(envelope.Data) != "[]" {
			t.Errorf("data = %s, want []", envelope.Data)
		}
	})

	t.Run("policy with no runs: latest_run is null", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				ID        string          `json:"id"`
				LatestRun json.RawMessage `json:"latest_run"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("got %d items, want 1", len(envelope.Data))
		}
		if string(envelope.Data[0].LatestRun) != "null" {
			t.Errorf("latest_run = %s, want null", envelope.Data[0].LatestRun)
		}
	})

	t.Run("policy with a run: latest_run is populated", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")
		insertTestRun(t, store, "run1", "pol1", "complete")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				LatestRun *struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"latest_run"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("got %d items, want 1", len(envelope.Data))
		}
		lr := envelope.Data[0].LatestRun
		if lr == nil {
			t.Fatal("latest_run is null, want populated")
		}
		if lr.ID != "run1" {
			t.Errorf("latest_run.id = %q, want run1", lr.ID)
		}
		if lr.Status != "complete" {
			t.Errorf("latest_run.status = %q, want complete", lr.Status)
		}
	})

	t.Run("folder extracted from YAML", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "folder: TestFolder\ntrigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				Folder string `json:"folder"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("got %d items, want 1", len(envelope.Data))
		}
		if envelope.Data[0].Folder != "TestFolder" {
			t.Errorf("folder = %q, want %q", envelope.Data[0].Folder, "TestFolder")
		}
	})
}

func TestPolicyListAvgTokenCost(t *testing.T) {
	t.Run("avg_token_cost is average of all runs for the policy", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")
		testutil.InsertRunWithTime(t, store, "run1", "pol1", model.RunStatusComplete, "2026-01-01T00:00:00Z", 1000)
		testutil.InsertRunWithTime(t, store, "run2", "pol1", model.RunStatusComplete, "2026-01-02T00:00:00Z", 3000)

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				ID           string `json:"id"`
				AvgTokenCost int64  `json:"avg_token_cost"`
				RunCount     int64  `json:"run_count"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("got %d items, want 1", len(envelope.Data))
		}
		if envelope.Data[0].AvgTokenCost != 2000 {
			t.Errorf("avg_token_cost = %d, want 2000", envelope.Data[0].AvgTokenCost)
		}
		if envelope.Data[0].RunCount != 2 {
			t.Errorf("run_count = %d, want 2", envelope.Data[0].RunCount)
		}
	})

	t.Run("avg_token_cost is 0 when policy has no runs", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				AvgTokenCost int64 `json:"avg_token_cost"`
				RunCount     int64 `json:"run_count"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("got %d items, want 1", len(envelope.Data))
		}
		if envelope.Data[0].AvgTokenCost != 0 {
			t.Errorf("avg_token_cost = %d, want 0", envelope.Data[0].AvgTokenCost)
		}
		if envelope.Data[0].RunCount != 0 {
			t.Errorf("run_count = %d, want 0", envelope.Data[0].RunCount)
		}
	})
}

func TestPolicyListModelAndToolCount(t *testing.T) {
	t.Run("model and tool_count extracted from YAML", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		yaml := `
model: claude-opus-4-5
trigger: webhook
capabilities:
  tools:
    - tool: github.list_repos
    - tool: github.create_issue
`
		insertTestPolicy(t, store, "pol1", "my-policy", yaml)

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				Model     string `json:"model"`
				ToolCount int    `json:"tool_count"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("got %d items, want 1", len(envelope.Data))
		}
		if envelope.Data[0].Model != "claude-opus-4-5" {
			t.Errorf("model = %q, want claude-opus-4-5", envelope.Data[0].Model)
		}
		if envelope.Data[0].ToolCount != 2 {
			t.Errorf("tool_count = %d, want 2", envelope.Data[0].ToolCount)
		}
	})

	t.Run("model is empty and tool_count is 0 when not in YAML", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				Model     string `json:"model"`
				ToolCount int    `json:"tool_count"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("got %d items, want 1", len(envelope.Data))
		}
		if envelope.Data[0].Model != "" {
			t.Errorf("model = %q, want empty string", envelope.Data[0].Model)
		}
		if envelope.Data[0].ToolCount != 0 {
			t.Errorf("tool_count = %d, want 0", envelope.Data[0].ToolCount)
		}
	})
}

func TestPolicyListToolRefs(t *testing.T) {
	t.Run("tool_refs extracted from capabilities YAML", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		yaml := `
model: claude-opus-4-5
trigger: webhook
capabilities:
  tools:
    - tool: github.list_repos
    - tool: github.create_issue
      approval: required
`
		insertTestPolicy(t, store, "pol1", "my-policy", yaml)

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []struct {
				ToolCount int      `json:"tool_count"`
				ToolRefs  []string `json:"tool_refs"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("got %d items, want 1", len(envelope.Data))
		}
		item := envelope.Data[0]
		if item.ToolCount != 2 {
			t.Errorf("tool_count = %d, want 2", item.ToolCount)
		}
		if len(item.ToolRefs) != 2 {
			t.Fatalf("tool_refs length = %d, want 2", len(item.ToolRefs))
		}
		if item.ToolRefs[0] != "github.list_repos" {
			t.Errorf("tool_refs[0] = %q, want github.list_repos", item.ToolRefs[0])
		}
		if item.ToolRefs[1] != "github.create_issue" {
			t.Errorf("tool_refs[1] = %q, want github.create_issue", item.ToolRefs[1])
		}
	})

	t.Run("tool_refs is empty array when no tools defined", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies")
		if err != nil {
			t.Fatalf("GET /policies: %v", err)
		}
		defer resp.Body.Close()

		var envelope struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("got %d items, want 1", len(envelope.Data))
		}
		// Verify tool_refs is "[]" not "null" in JSON.
		raw := string(envelope.Data[0])
		if !strings.Contains(raw, `"tool_refs":[]`) {
			t.Errorf("expected tool_refs:[] in JSON, got: %s", raw)
		}
	})
}

func TestPolicyGetHandler(t *testing.T) {
	t.Run("valid ID returns 200 with policy detail", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "folder: Ops\ntrigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies/pol1")
		if err != nil {
			t.Fatalf("GET /policies/pol1: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				TriggerType string `json:"trigger_type"`
				Folder      string `json:"folder"`
				YAML        string `json:"yaml"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		d := envelope.Data
		if d.ID != "pol1" {
			t.Errorf("id = %q, want pol1", d.ID)
		}
		if d.Name != "my-policy" {
			t.Errorf("name = %q, want my-policy", d.Name)
		}
		if d.TriggerType != "webhook" {
			t.Errorf("trigger_type = %q, want webhook", d.TriggerType)
		}
		if d.Folder != "Ops" {
			t.Errorf("folder = %q, want Ops", d.Folder)
		}
		if d.YAML != "folder: Ops\ntrigger: webhook\n" {
			t.Errorf("yaml = %q", d.YAML)
		}
	})

	t.Run("missing ID returns 404", func(t *testing.T) {
		store := newPolicyHandlerStore(t)

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/policies/does-not-exist")
		if err != nil {
			t.Fatalf("GET /policies/does-not-exist: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}

		var envelope struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Error != "policy not found" {
			t.Errorf("error = %q, want %q", envelope.Error, "policy not found")
		}
	})
}

// validPolicyYAML is a minimal valid policy used across mutation handler tests.
const validPolicyYAML = `
name: test-policy
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: github.list_repos
agent:
  task: Check all repos
`

func TestPolicyCreateHandler(t *testing.T) {
	t.Run("valid YAML returns 201 with policy and empty warnings", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader(validPolicyYAML))
		if err != nil {
			t.Fatalf("POST /policies: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				ID       string   `json:"id"`
				Name     string   `json:"name"`
				Warnings []string `json:"warnings"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.ID == "" {
			t.Error("expected non-empty id")
		}
		if envelope.Data.Name != "test-policy" {
			t.Errorf("name = %q, want test-policy", envelope.Data.Name)
		}
		if envelope.Data.Warnings == nil {
			t.Error("warnings must not be null — want empty array")
		}
		if len(envelope.Data.Warnings) != 0 {
			t.Errorf("warnings = %v, want []", envelope.Data.Warnings)
		}
		wantLocation := "/api/v1/policies/" + envelope.Data.ID
		if loc := resp.Header.Get("Location"); loc != wantLocation {
			t.Errorf("Location = %q, want %q", loc, wantLocation)
		}
	})

	t.Run("invalid YAML returns 400", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader("{{bad yaml"))
		if err != nil {
			t.Fatalf("POST /policies: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("unresolvable tool refs returns 201 with warnings", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouterWithLookup(store, alwaysMissingLookup{}))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader(validPolicyYAML))
		if err != nil {
			t.Fatalf("POST /policies: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				Warnings []string `json:"warnings"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(envelope.Data.Warnings) == 0 {
			t.Fatal("expected at least one warning, got none")
		}
		found := false
		for _, w := range envelope.Data.Warnings {
			if strings.Contains(w, "github.list_repos") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("warnings %v do not mention github.list_repos", envelope.Data.Warnings)
		}
	})

	t.Run("validation error returns 400 with detail", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		// Missing name field triggers validation failure.
		noNameYAML := `
trigger:
  type: webhook
capabilities:
  tools:
    - tool: github.list_repos
agent:
  task: Check all repos
`
		resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader(noNameYAML))
		if err != nil {
			t.Fatalf("POST /policies: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}

		var envelope struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Detail == "" {
			t.Error("expected non-empty detail for validation error")
		}
	})
}

func TestPolicyUpdateHandler(t *testing.T) {
	t.Run("valid update returns 200 with updated fields", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "original-name", "trigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		updatedYAML := `
name: updated-name
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: github.list_repos
agent:
  task: Updated task
`
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/policies/pol1", strings.NewReader(updatedYAML))
		req.Header.Set("Content-Type", "application/yaml")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT /policies/pol1: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.Name != "updated-name" {
			t.Errorf("name = %q, want updated-name", envelope.Data.Name)
		}
	})

	t.Run("update non-existent policy returns 404", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/policies/does-not-exist", strings.NewReader(validPolicyYAML))
		req.Header.Set("Content-Type", "application/yaml")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT /policies/does-not-exist: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("validation error on update returns 400", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/policies/pol1", strings.NewReader("{{bad yaml"))
		req.Header.Set("Content-Type", "application/yaml")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT /policies/pol1: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}

func TestPolicyDeleteHandler(t *testing.T) {
	t.Run("delete existing policy returns 204", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/policies/pol1", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /policies/pol1: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}
	})

	t.Run("delete non-existent policy returns 404", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/policies/does-not-exist", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /policies/does-not-exist: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("delete with active run returns 409", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")
		insertTestRun(t, store, "run1", "pol1", "running")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/policies/pol1", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /policies/pol1: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
	})

	t.Run("delete with completed run succeeds", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")
		insertTestRun(t, store, "run1", "pol1", "complete")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/policies/pol1", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /policies/pol1: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}
	})

	t.Run("delete cascades to run, run_steps, and approval_requests", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")
		insertTestRun(t, store, "run1", "pol1", "complete")
		testutil.InsertRunStep(t, store, "step1", "run1", 1)
		testutil.InsertApprovalRequest(t, store, "apr1", "run1", "some_tool")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/policies/pol1", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /policies/pol1: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}

		// Verify all records are gone.
		var n int
		db := store.DB()

		if err := db.QueryRow(`SELECT COUNT(*) FROM policies WHERE id = 'pol1'`).Scan(&n); err != nil {
			t.Fatalf("query policies: %v", err)
		}
		if n != 0 {
			t.Errorf("policies: got %d rows, want 0", n)
		}

		if err := db.QueryRow(`SELECT COUNT(*) FROM runs WHERE id = 'run1'`).Scan(&n); err != nil {
			t.Fatalf("query runs: %v", err)
		}
		if n != 0 {
			t.Errorf("runs: got %d rows, want 0", n)
		}

		if err := db.QueryRow(`SELECT COUNT(*) FROM run_steps WHERE id = 'step1'`).Scan(&n); err != nil {
			t.Fatalf("query run_steps: %v", err)
		}
		if n != 0 {
			t.Errorf("run_steps: got %d rows, want 0", n)
		}

		if err := db.QueryRow(`SELECT COUNT(*) FROM approval_requests WHERE id = 'apr1'`).Scan(&n); err != nil {
			t.Fatalf("query approval_requests: %v", err)
		}
		if n != 0 {
			t.Errorf("approval_requests: got %d rows, want 0", n)
		}
	})
}

func TestPolicyCRUDRoundTrip(t *testing.T) {
	store := newPolicyHandlerStore(t)
	srv := httptest.NewServer(newPolicyRouter(store))
	t.Cleanup(srv.Close)

	// POST — create
	resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader(validPolicyYAML))
	if err != nil {
		t.Fatalf("POST /policies: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status = %d, want 201", resp.StatusCode)
	}
	var createEnvelope struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createEnvelope); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	id := createEnvelope.Data.ID
	if id == "" {
		t.Fatal("create returned empty id")
	}

	// GET — fetch it back
	getResp, err := http.Get(srv.URL + "/policies/" + id)
	if err != nil {
		t.Fatalf("GET /policies/%s: %v", id, err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get: status = %d, want 200", getResp.StatusCode)
	}

	// PUT — update name
	updatedYAML := `
name: updated-policy
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: github.list_repos
agent:
  task: Updated task
`
	putReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/policies/"+id, strings.NewReader(updatedYAML))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT /policies/%s: %v", id, err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("update: status = %d, want 200", putResp.StatusCode)
	}
	var updateEnvelope struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(putResp.Body).Decode(&updateEnvelope); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateEnvelope.Data.Name != "updated-policy" {
		t.Errorf("after update name = %q, want updated-policy", updateEnvelope.Data.Name)
	}

	// LIST — policy appears in list
	listResp, err := http.Get(srv.URL + "/policies")
	if err != nil {
		t.Fatalf("GET /policies: %v", err)
	}
	defer listResp.Body.Close()
	var listEnvelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listEnvelope); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	found := false
	for _, item := range listEnvelope.Data {
		if item.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("policy %s not found in list", id)
	}

	// DELETE — remove it
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/policies/"+id, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE /policies/%s: %v", id, err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", delResp.StatusCode)
	}

	// GET after DELETE — must 404
	getAfterResp, err := http.Get(srv.URL + "/policies/" + id)
	if err != nil {
		t.Fatalf("GET /policies/%s after delete: %v", id, err)
	}
	defer getAfterResp.Body.Close()
	if getAfterResp.StatusCode != http.StatusNotFound {
		t.Fatalf("get-after-delete: status = %d, want 404", getAfterResp.StatusCode)
	}
}

// TestParsePolicySummary_InvalidYAML verifies that a policy row whose YAML blob
// is corrupt does not crash the list endpoint — it should still return 200 with
// zero-value summary fields for that row.
func TestParsePolicySummary_InvalidYAML(t *testing.T) {
	store := newPolicyHandlerStore(t)
	// Insert a policy with deliberately malformed YAML directly so we bypass
	// policy.Service validation (which would normally reject bad YAML).
	insertTestPolicy(t, store, "bad-yaml-id", "corrupt-policy", "{{not: valid: yaml: [}")

	srv := httptest.NewServer(newPolicyRouter(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/policies")
	if err != nil {
		t.Fatalf("GET /policies: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; corrupt YAML must not crash the list endpoint", resp.StatusCode)
	}

	var envelope struct {
		Data []struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("got %d items, want 1", len(envelope.Data))
	}
	if envelope.Data[0].ID != "bad-yaml-id" {
		t.Errorf("id = %q, want %q", envelope.Data[0].ID, "bad-yaml-id")
	}
	// Model should be empty string (zero value) when YAML parse fails.
	if envelope.Data[0].Model != "" {
		t.Errorf("model = %q, want empty string for corrupt YAML", envelope.Data[0].Model)
	}
}

// setPolicyPaused sets paused_at on a policy row directly via the store.
func setPolicyPaused(t *testing.T, s *db.Store, id string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.SetPolicyPausedAt(context.Background(), db.SetPolicyPausedAtParams{
		PausedAt: &now,
		ID:       id,
	}); err != nil {
		t.Fatalf("SetPolicyPausedAt %s: %v", id, err)
	}
}

func TestPolicyPauseHandler(t *testing.T) {
	t.Run("pause sets paused_at and returns 200", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies/pol1/pause", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /policies/pol1/pause: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				ID       string  `json:"id"`
				PausedAt *string `json:"paused_at"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.ID != "pol1" {
			t.Errorf("id = %q, want pol1", envelope.Data.ID)
		}
		if envelope.Data.PausedAt == nil {
			t.Error("paused_at is null, want non-null after pause")
		}
	})

	t.Run("pause returns 404 for unknown policy", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies/no-such-id/pause", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /policies/no-such-id/pause: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("pause returns 409 when already paused", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")
		setPolicyPaused(t, store, "pol1")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies/pol1/pause", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /policies/pol1/pause: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}

		var envelope struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Error != "policy is already paused" {
			t.Errorf("error = %q, want %q", envelope.Error, "policy is already paused")
		}
	})
}

func TestPolicyResumeHandler(t *testing.T) {
	t.Run("resume clears paused_at and returns 200", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")
		setPolicyPaused(t, store, "pol1")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies/pol1/resume", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /policies/pol1/resume: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				ID       string  `json:"id"`
				PausedAt *string `json:"paused_at"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.ID != "pol1" {
			t.Errorf("id = %q, want pol1", envelope.Data.ID)
		}
		if envelope.Data.PausedAt != nil {
			t.Errorf("paused_at = %q, want null after resume", *envelope.Data.PausedAt)
		}
	})

	t.Run("resume returns 404 for unknown policy", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies/no-such-id/resume", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /policies/no-such-id/resume: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("resume returns 409 when not paused", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "pol1", "my-policy", "trigger: webhook\n")

		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies/pol1/resume", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /policies/pol1/resume: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}

		var envelope struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Error != "policy is not paused" {
			t.Errorf("error = %q, want %q", envelope.Error, "policy is not paused")
		}
	})
}

// ---- Notifier wiring tests -------------------------------------------------

// recordingNotifier implements api.PolicyNotifier and records each Notify call.
type recordingNotifier struct {
	mu       sync.Mutex
	notified []string // policyIDs in call order
}

func (r *recordingNotifier) Notify(_ context.Context, policyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notified = append(r.notified, policyID)
}

func (r *recordingNotifier) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.notified))
	copy(out, r.notified)
	return out
}

// newPolicyRouterWithNotifiers wires a chi router with the policy handler and
// the given notifiers. All may be nil.
func newPolicyRouterWithNotifiers(store *db.Store, poller, scheduler, cron api.PolicyNotifier) http.Handler {
	r := chi.NewRouter()
	svc := policy.NewService(store, nil, nil, nil, nil)
	h := api.NewPolicyHandler(store, svc, poller, scheduler, cron)
	r.Get("/policies", h.List)
	r.Post("/policies", h.Create)
	r.Get("/policies/{id}", h.Get)
	r.Put("/policies/{id}", h.Update)
	r.Post("/policies/{id}/pause", h.Pause)
	r.Post("/policies/{id}/resume", h.Resume)
	r.Delete("/policies/{id}", h.Delete)
	return r
}

const pollPolicyYAMLForHandler = `
name: my-poll
trigger:
  type: poll
  interval: 1m
  match: all
  checks:
    - tool: srv.check
      path: "$.ok"
      equals: "true"
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.check
agent:
  task: "poll task"
`

const scheduledPolicyYAMLForHandler = `
name: my-scheduled
trigger:
  type: scheduled
  fire_at:
    - "2099-01-01T00:00:00Z"
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.check
agent:
  task: "scheduled task"
`

const webhookPolicyYAMLForHandler = `
name: my-webhook
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.check
agent:
  task: "webhook task"
`

// TestPolicyHandler_NotifiesPollerOnCreate verifies that creating a poll policy
// calls poller.Notify exactly once with the new policy ID, and does not call
// scheduler.Notify.
func TestPolicyHandler_NotifiesPollerOnCreate(t *testing.T) {
	store := newPolicyHandlerStore(t)
	poller := &recordingNotifier{}
	scheduler := &recordingNotifier{}
	srv := httptest.NewServer(newPolicyRouterWithNotifiers(store, poller, scheduler, nil))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader(pollPolicyYAMLForHandler))
	if err != nil {
		t.Fatalf("POST /policies: %v", err)
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
		t.Fatalf("decode: %v", err)
	}

	pollerCalls := poller.calls()
	if len(pollerCalls) != 1 {
		t.Fatalf("poller.Notify called %d times, want 1", len(pollerCalls))
	}
	if pollerCalls[0] != envelope.Data.ID {
		t.Errorf("poller notified with %q, want %q", pollerCalls[0], envelope.Data.ID)
	}
	if len(scheduler.calls()) != 0 {
		t.Errorf("scheduler.Notify called %d times, want 0", len(scheduler.calls()))
	}
}

// TestPolicyHandler_NotifiesSchedulerOnCreate verifies that creating a
// scheduled policy calls scheduler.Notify and not poller.Notify.
func TestPolicyHandler_NotifiesSchedulerOnCreate(t *testing.T) {
	store := newPolicyHandlerStore(t)
	poller := &recordingNotifier{}
	scheduler := &recordingNotifier{}
	srv := httptest.NewServer(newPolicyRouterWithNotifiers(store, poller, scheduler, nil))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader(scheduledPolicyYAMLForHandler))
	if err != nil {
		t.Fatalf("POST /policies: %v", err)
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
		t.Fatalf("decode: %v", err)
	}

	schedCalls := scheduler.calls()
	if len(schedCalls) != 1 {
		t.Fatalf("scheduler.Notify called %d times, want 1", len(schedCalls))
	}
	if schedCalls[0] != envelope.Data.ID {
		t.Errorf("scheduler notified with %q, want %q", schedCalls[0], envelope.Data.ID)
	}
	if len(poller.calls()) != 0 {
		t.Errorf("poller.Notify called %d times, want 0", len(poller.calls()))
	}
}

// TestPolicyHandler_NoNotifyForWebhookPolicy verifies that webhook and manual
// trigger types do not invoke either notifier.
func TestPolicyHandler_NoNotifyForWebhookPolicy(t *testing.T) {
	store := newPolicyHandlerStore(t)
	poller := &recordingNotifier{}
	scheduler := &recordingNotifier{}
	srv := httptest.NewServer(newPolicyRouterWithNotifiers(store, poller, scheduler, nil))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader(webhookPolicyYAMLForHandler))
	if err != nil {
		t.Fatalf("POST /policies: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	if len(poller.calls()) != 0 {
		t.Errorf("poller.Notify called %d times, want 0 for webhook policy", len(poller.calls()))
	}
	if len(scheduler.calls()) != 0 {
		t.Errorf("scheduler.Notify called %d times, want 0 for webhook policy", len(scheduler.calls()))
	}
}

// TestPolicyHandler_NotifiesPollerOnUpdate verifies that updating a poll policy
// calls poller.Notify.
func TestPolicyHandler_NotifiesPollerOnUpdate(t *testing.T) {
	store := newPolicyHandlerStore(t)
	testutil.InsertPolicy(t, store, "pol-upd-poll", "my-poll", "poll", pollPolicyYAMLForHandler)
	poller := &recordingNotifier{}
	scheduler := &recordingNotifier{}
	srv := httptest.NewServer(newPolicyRouterWithNotifiers(store, poller, scheduler, nil))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/policies/pol-upd-poll", strings.NewReader(pollPolicyYAMLForHandler))
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /policies/pol-upd-poll: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if len(poller.calls()) != 1 || poller.calls()[0] != "pol-upd-poll" {
		t.Errorf("poller.Notify calls = %v, want [pol-upd-poll]", poller.calls())
	}
	if len(scheduler.calls()) != 0 {
		t.Errorf("scheduler.Notify called unexpectedly: %v", scheduler.calls())
	}
}

// TestPolicyHandler_NotifiesOnPauseAndResume verifies that pausing and resuming
// a poll policy calls poller.Notify each time.
func TestPolicyHandler_NotifiesOnPauseAndResume(t *testing.T) {
	store := newPolicyHandlerStore(t)
	testutil.InsertPolicy(t, store, "pol-pr", "my-poll", "poll", pollPolicyYAMLForHandler)
	poller := &recordingNotifier{}
	scheduler := &recordingNotifier{}
	srv := httptest.NewServer(newPolicyRouterWithNotifiers(store, poller, scheduler, nil))
	t.Cleanup(srv.Close)

	// Pause.
	resp, err := http.Post(srv.URL+"/policies/pol-pr/pause", "application/json", nil)
	if err != nil {
		t.Fatalf("POST pause: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d, want 200", resp.StatusCode)
	}

	// Resume.
	resp2, err := http.Post(srv.URL+"/policies/pol-pr/resume", "application/json", nil)
	if err != nil {
		t.Fatalf("POST resume: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d, want 200", resp2.StatusCode)
	}

	calls := poller.calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 poller.Notify calls (pause + resume), got %d: %v", len(calls), calls)
	}
	for _, id := range calls {
		if id != "pol-pr" {
			t.Errorf("notified with unexpected id %q", id)
		}
	}
}

// TestPolicyHandler_NotifiesPollerOnDelete verifies that deleting a poll policy
// calls poller.Notify with the policy ID.
func TestPolicyHandler_NotifiesPollerOnDelete(t *testing.T) {
	store := newPolicyHandlerStore(t)
	testutil.InsertPolicy(t, store, "pol-del-poll", "my-poll", "poll", pollPolicyYAMLForHandler)
	poller := &recordingNotifier{}
	scheduler := &recordingNotifier{}
	srv := httptest.NewServer(newPolicyRouterWithNotifiers(store, poller, scheduler, nil))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/policies/pol-del-poll", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /policies/pol-del-poll: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	if len(poller.calls()) != 1 || poller.calls()[0] != "pol-del-poll" {
		t.Errorf("poller.Notify calls = %v, want [pol-del-poll]", poller.calls())
	}
	if len(scheduler.calls()) != 0 {
		t.Errorf("scheduler.Notify called unexpectedly: %v", scheduler.calls())
	}
}

const cronPolicyYAMLForHandler = `
name: my-cron
trigger:
  type: cron
  cron_expr: "0 9 * * 1"
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.check
agent:
  task: "cron task"
`

// TestPolicyHandler_NotifiesCronRunnerOnCreate verifies that creating a cron
// policy calls cron.Notify exactly once with the new policy ID, and does not
// call poller.Notify or scheduler.Notify.
func TestPolicyHandler_NotifiesCronRunnerOnCreate(t *testing.T) {
	store := newPolicyHandlerStore(t)
	poller := &recordingNotifier{}
	scheduler := &recordingNotifier{}
	cron := &recordingNotifier{}
	srv := httptest.NewServer(newPolicyRouterWithNotifiers(store, poller, scheduler, cron))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader(cronPolicyYAMLForHandler))
	if err != nil {
		t.Fatalf("POST /policies: %v", err)
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
		t.Fatalf("decode: %v", err)
	}

	cronCalls := cron.calls()
	if len(cronCalls) != 1 {
		t.Fatalf("cron.Notify called %d times, want 1", len(cronCalls))
	}
	if cronCalls[0] != envelope.Data.ID {
		t.Errorf("cron notified with %q, want %q", cronCalls[0], envelope.Data.ID)
	}
	if len(poller.calls()) != 0 {
		t.Errorf("poller.Notify called %d times, want 0", len(poller.calls()))
	}
	if len(scheduler.calls()) != 0 {
		t.Errorf("scheduler.Notify called %d times, want 0", len(scheduler.calls()))
	}
}

// TestPolicyHandler_NotifiesCronRunnerOnUpdate verifies that updating a cron
// policy calls cron.Notify and does not call poller.Notify or scheduler.Notify.
func TestPolicyHandler_NotifiesCronRunnerOnUpdate(t *testing.T) {
	store := newPolicyHandlerStore(t)
	testutil.InsertPolicy(t, store, "pol-upd-cron", "my-cron", "cron", cronPolicyYAMLForHandler)
	poller := &recordingNotifier{}
	scheduler := &recordingNotifier{}
	cron := &recordingNotifier{}
	srv := httptest.NewServer(newPolicyRouterWithNotifiers(store, poller, scheduler, cron))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/policies/pol-upd-cron", strings.NewReader(cronPolicyYAMLForHandler))
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /policies/pol-upd-cron: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if len(cron.calls()) != 1 || cron.calls()[0] != "pol-upd-cron" {
		t.Errorf("cron.Notify calls = %v, want [pol-upd-cron]", cron.calls())
	}
	if len(poller.calls()) != 0 {
		t.Errorf("poller.Notify called unexpectedly: %v", poller.calls())
	}
	if len(scheduler.calls()) != 0 {
		t.Errorf("scheduler.Notify called unexpectedly: %v", scheduler.calls())
	}
}

// TestPolicyHandler_NotifiesCronRunnerOnDelete verifies that deleting a cron
// policy calls cron.Notify with the policy ID.
func TestPolicyHandler_NotifiesCronRunnerOnDelete(t *testing.T) {
	store := newPolicyHandlerStore(t)
	testutil.InsertPolicy(t, store, "pol-del-cron", "my-cron", "cron", cronPolicyYAMLForHandler)
	poller := &recordingNotifier{}
	scheduler := &recordingNotifier{}
	cron := &recordingNotifier{}
	srv := httptest.NewServer(newPolicyRouterWithNotifiers(store, poller, scheduler, cron))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/policies/pol-del-cron", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /policies/pol-del-cron: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	if len(cron.calls()) != 1 || cron.calls()[0] != "pol-del-cron" {
		t.Errorf("cron.Notify calls = %v, want [pol-del-cron]", cron.calls())
	}
	if len(poller.calls()) != 0 {
		t.Errorf("poller.Notify called unexpectedly: %v", poller.calls())
	}
	if len(scheduler.calls()) != 0 {
		t.Errorf("scheduler.Notify called unexpectedly: %v", scheduler.calls())
	}
}

// TestPolicyHandler_NilNotifiersDoNotPanic verifies that nil poller and
// scheduler do not cause a panic — the defensive nil check in notifyTriggers
// must hold for all handler paths.
func TestPolicyHandler_NilNotifiersDoNotPanic(t *testing.T) {
	store := newPolicyHandlerStore(t)
	testutil.InsertPolicy(t, store, "pol-nil-poll", "my-poll", "poll", pollPolicyYAMLForHandler)

	// newPolicyRouter passes nil, nil — reuse it.
	srv := httptest.NewServer(newPolicyRouter(store))
	t.Cleanup(srv.Close)

	// Create.
	resp, err := http.Post(srv.URL+"/policies", "application/yaml", strings.NewReader(pollPolicyYAMLForHandler))
	if err != nil {
		t.Fatalf("POST /policies: %v", err)
	}
	resp.Body.Close()

	// Pause.
	resp, err = http.Post(srv.URL+"/policies/pol-nil-poll/pause", "application/json", nil)
	if err != nil {
		t.Fatalf("POST pause: %v", err)
	}
	resp.Body.Close()

	// Resume.
	resp, err = http.Post(srv.URL+"/policies/pol-nil-poll/resume", "application/json", nil)
	if err != nil {
		t.Fatalf("POST resume: %v", err)
	}
	resp.Body.Close()

	// Update.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/policies/pol-nil-poll", strings.NewReader(pollPolicyYAMLForHandler))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /policies/pol-nil-poll: %v", err)
	}
	resp.Body.Close()

	// Delete.
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/policies/pol-nil-poll", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /policies/pol-nil-poll: %v", err)
	}
	resp.Body.Close()

	// If we reach here without panicking, the test passes.
}

// TestPolicyValidationErrorShape asserts that the API returns the structured
// `issues` array alongside the legacy `detail` string when policy validation
// fails. This covers five distinct field families so the handler's mapping from
// policy.Issue to httputil.ErrorIssue is exercised end-to-end.
func TestPolicyValidationErrorShape(t *testing.T) {
	// responseEnvelope captures the fields we want to assert on.
	type issueJSON struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	}
	type envelope struct {
		Error  string      `json:"error"`
		Detail string      `json:"detail"`
		Issues []issueJSON `json:"issues"`
	}

	mustDecodeEnvelope := func(t *testing.T, body []byte) envelope {
		t.Helper()
		var env envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return env
	}

	findIssue := func(issues []issueJSON, field, messageSubstr string) bool {
		for _, iss := range issues {
			if iss.Field == field && strings.Contains(iss.Message, messageSubstr) {
				return true
			}
		}
		return false
	}

	// assertValidationEnvelope POSTs yaml and checks the structured response.
	assertValidationEnvelope := func(t *testing.T, yaml string, wantIssues []issueJSON) {
		t.Helper()
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/policies", "text/plain", strings.NewReader(yaml))
		if err != nil {
			t.Fatalf("POST /policies: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}

		var rawBody []byte
		rawBody, err = io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}

		env := mustDecodeEnvelope(t, rawBody)

		if env.Error != "policy validation failed" {
			t.Errorf("error = %q, want %q", env.Error, "policy validation failed")
		}
		if env.Detail == "" {
			t.Errorf("detail must not be empty (legacy shape preserved)")
		}
		if len(env.Issues) == 0 {
			t.Errorf("issues must not be empty, got: %s", rawBody)
		}

		for _, want := range wantIssues {
			if !findIssue(env.Issues, want.Field, want.Message) {
				t.Errorf("missing issue {field=%q, message~=%q} in %v", want.Field, want.Message, env.Issues)
			}
		}
	}

	t.Run("missing name", func(t *testing.T) {
		yaml := `
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
agent:
  task: do something
  limits:
    max_tokens_per_run: 1000
    max_tool_calls_per_run: 10
  concurrency: skip
`
		assertValidationEnvelope(t, yaml, []issueJSON{
			{Field: "name", Message: "name is required"},
		})
	})

	t.Run("missing tool dot-notation", func(t *testing.T) {
		yaml := `
name: test
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: nodot
agent:
  task: do something
  limits:
    max_tokens_per_run: 1000
    max_tool_calls_per_run: 10
  concurrency: skip
`
		assertValidationEnvelope(t, yaml, []issueJSON{
			{Field: "capabilities.tools[0].tool", Message: "dot notation"},
		})
	})

	t.Run("invalid concurrency mode", func(t *testing.T) {
		yaml := `
name: test
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
agent:
  task: do something
  limits:
    max_tokens_per_run: 1000
    max_tool_calls_per_run: 10
  concurrency: blorp
`
		assertValidationEnvelope(t, yaml, []issueJSON{
			{Field: "agent.concurrency", Message: "invalid"},
		})
	})

	t.Run("negative max_tokens_per_run", func(t *testing.T) {
		yaml := `
name: test
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
agent:
  task: do something
  limits:
    max_tokens_per_run: -1
    max_tool_calls_per_run: 10
  concurrency: skip
`
		assertValidationEnvelope(t, yaml, []issueJSON{
			{Field: "agent.limits.max_tokens_per_run", Message: "must be zero (unlimited) or positive"},
		})
	})

	t.Run("replace plus approval conflict", func(t *testing.T) {
		yaml := `
name: test
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
      approval: required
      on_timeout: reject
agent:
  task: do something
  limits:
    max_tokens_per_run: 1000
    max_tool_calls_per_run: 10
  concurrency: replace
`
		assertValidationEnvelope(t, yaml, []issueJSON{
			{Field: "agent.concurrency", Message: "replace"},
		})
	})
}

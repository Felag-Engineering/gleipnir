package api_test

import (
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
	"github.com/rapp992/gleipnir/internal/policy"
)

// newPolicyHandlerStore opens a fresh migrated store for handler tests.
func newPolicyHandlerStore(t *testing.T) *db.Store {
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

// newPolicyRouter wires a chi router with the policy handler, mirroring how
// NewRouter mounts the routes in production.
func newPolicyRouter(store *db.Store) http.Handler {
	r := chi.NewRouter()
	svc := policy.NewService(store, nil)
	h := api.NewPolicyHandler(store, svc)
	r.Get("/policies", h.List)
	r.Post("/policies", h.Create)
	r.Get("/policies/{id}", h.Get)
	r.Put("/policies/{id}", h.Update)
	r.Delete("/policies/{id}", h.Delete)
	return r
}

// insertTestPolicy inserts a policy row directly via the store.
func insertTestPolicy(t *testing.T, s *db.Store, id, name, yaml string) {
	t.Helper()
	_, err := s.CreatePolicy(context.Background(), db.CreatePolicyParams{
		ID:          id,
		Name:        name,
		TriggerType: "webhook",
		Yaml:        yaml,
		CreatedAt:   "2024-01-01T00:00:00Z",
		UpdatedAt:   "2024-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("insertTestPolicy %s: %v", id, err)
	}
}

// insertTestRun inserts a run row directly via the store.
func insertTestRun(t *testing.T, s *db.Store, id, policyID, status string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
		 VALUES (?, ?, ?, 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		id, policyID, status,
	)
	if err != nil {
		t.Fatalf("insertTestRun %s: %v", id, err)
	}
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
capabilities:
  sensors:
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

	t.Run("validation error returns 400 with detail", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		srv := httptest.NewServer(newPolicyRouter(store))
		t.Cleanup(srv.Close)

		// Missing name field triggers validation failure.
		noNameYAML := `
trigger:
  type: webhook
capabilities:
  sensors:
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
capabilities:
  sensors:
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
capabilities:
  sensors:
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

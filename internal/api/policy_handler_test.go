package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
)

// newPolicyHandlerStore opens a fresh migrated store for handler tests.
func newPolicyHandlerStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// newPolicyRouter wires a chi router with the policy handler, mirroring how
// NewRouter mounts the routes in production.
func newPolicyRouter(store *db.Store) http.Handler {
	r := chi.NewRouter()
	h := api.NewPolicyHandler(store)
	r.Get("/policies", h.List)
	r.Get("/policies/{id}", h.Get)
	return r
}

// insertTestPolicy inserts a policy row directly via the store.
func insertTestPolicy(t *testing.T, s *db.Store, id, name, yaml string) {
	t.Helper()
	_, err := s.CreatePolicy(t.Context(), db.CreatePolicyParams{
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

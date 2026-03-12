package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapp992/gleipnir/internal/api"
)

func TestStatsHandler(t *testing.T) {
	t.Run("GET /stats returns 200 with all fields on empty db", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		svc := api.NewStatsService(store)
		r := chi.NewRouter()
		h := api.NewStatsHandler(svc)
		r.Get("/stats", h.Get)
		srv := httptest.NewServer(r)
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/stats")
		if err != nil {
			t.Fatalf("GET /stats: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				ActiveRuns       int64 `json:"active_runs"`
				PendingApprovals int64 `json:"pending_approvals"`
				PolicyCount      int64 `json:"policy_count"`
				TokensLast24h    int64 `json:"tokens_last_24h"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		d := envelope.Data
		if d.ActiveRuns != 0 || d.PendingApprovals != 0 || d.PolicyCount != 0 || d.TokensLast24h != 0 {
			t.Errorf("expected all zeros on empty db, got %+v", d)
		}
	})

	t.Run("GET /stats reflects inserted data", func(t *testing.T) {
		store := newPolicyHandlerStore(t)
		insertTestPolicy(t, store, "p1", "pol1", "")
		insertTestRun(t, store, "r1", "p1", "running")

		svc := api.NewStatsService(store)
		r := chi.NewRouter()
		h := api.NewStatsHandler(svc)
		r.Get("/stats", h.Get)
		srv := httptest.NewServer(r)
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/stats")
		if err != nil {
			t.Fatalf("GET /stats: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data struct {
				ActiveRuns  int64 `json:"active_runs"`
				PolicyCount int64 `json:"policy_count"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if envelope.Data.PolicyCount != 1 {
			t.Errorf("PolicyCount = %d, want 1", envelope.Data.PolicyCount)
		}
		if envelope.Data.ActiveRuns != 1 {
			t.Errorf("ActiveRuns = %d, want 1", envelope.Data.ActiveRuns)
		}
	})
}

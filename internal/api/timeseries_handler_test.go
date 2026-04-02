package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/testutil"
)

func TestTimeSeriesHandlerEmptyDB(t *testing.T) {
	store := newPolicyHandlerStore(t)
	h := api.NewTimeSeriesHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window=24h&bucket=1h", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Data api.TimeSeriesResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// 24h window with 1h buckets should produce 24 buckets.
	if len(env.Data.Buckets) != 24 {
		t.Errorf("expected 24 buckets for 24h/1h window, got %d", len(env.Data.Buckets))
	}

	// All buckets should be zero.
	for i, b := range env.Data.Buckets {
		if b.Completed != 0 || b.Failed != 0 || b.WaitingForApproval != 0 {
			t.Errorf("bucket %d: expected all zeros, got %+v", i, b)
		}
		if len(b.CostByModel) != 0 {
			t.Errorf("bucket %d: expected empty cost_by_model, got %v", i, b.CostByModel)
		}
	}
}

func TestTimeSeriesHandlerStatusCategorization(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "pol1", "")

	// Insert runs with different statuses, all within the last hour so they
	// fall into the current bucket.
	recent := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano)
	testutil.InsertRunWithTime(t, store, "r-complete", "p1", "complete", recent, 500)
	testutil.InsertRunWithTime(t, store, "r-failed", "p1", "failed", recent, 100)
	testutil.InsertRunWithTime(t, store, "r-approval", "p1", "waiting_for_approval", recent, 0)

	h := api.NewTimeSeriesHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window=24h&bucket=1h", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var env struct {
		Data api.TimeSeriesResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Sum up all status counts across all buckets to verify totals.
	var totalCompleted, totalFailed, totalApproval int64
	for _, b := range env.Data.Buckets {
		totalCompleted += b.Completed
		totalFailed += b.Failed
		totalApproval += b.WaitingForApproval
	}

	if totalCompleted != 1 {
		t.Errorf("total completed = %d, want 1", totalCompleted)
	}
	if totalFailed != 1 {
		t.Errorf("total failed = %d, want 1", totalFailed)
	}
	if totalApproval != 1 {
		t.Errorf("total waiting_for_approval = %d, want 1", totalApproval)
	}
}

func TestTimeSeriesHandlerModelDisplayNames(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "pol1", "")

	recent := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano)

	// Insert runs with different model IDs — they should appear as display names.
	store.DB().Exec(
		`INSERT INTO runs(id, policy_id, model, status, trigger_type, trigger_payload, started_at, created_at, token_cost)
		 VALUES ('r-s4', 'p1', 'claude-sonnet-4-6', 'complete', 'webhook', '{}', ?, ?, 1000)`,
		recent, recent,
	)
	store.DB().Exec(
		`INSERT INTO runs(id, policy_id, model, status, trigger_type, trigger_payload, started_at, created_at, token_cost)
		 VALUES ('r-h35', 'p1', 'claude-haiku-3-5-20241022', 'complete', 'webhook', '{}', ?, ?, 500)`,
		recent, recent,
	)

	h := api.NewTimeSeriesHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window=24h&bucket=1h", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var env struct {
		Data api.TimeSeriesResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Merge cost_by_model across all buckets.
	merged := map[string]int64{}
	for _, b := range env.Data.Buckets {
		for k, v := range b.CostByModel {
			merged[k] += v
		}
	}

	// Keys should use display names, not API model IDs.
	if _, ok := merged["claude-sonnet-4-6"]; ok {
		t.Error("expected display name 'Sonnet 4', got raw API ID 'claude-sonnet-4-6'")
	}
	if merged["Sonnet 4"] != 1000 {
		t.Errorf("Sonnet 4 tokens = %d, want 1000", merged["Sonnet 4"])
	}
	if merged["Haiku 3.5"] != 500 {
		t.Errorf("Haiku 3.5 tokens = %d, want 500", merged["Haiku 3.5"])
	}
}

func TestTimeSeriesHandlerWindowParam7d(t *testing.T) {
	store := newPolicyHandlerStore(t)

	h := api.NewTimeSeriesHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window=7d&bucket=6h", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var env struct {
		Data api.TimeSeriesResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// 7d / 6h = 28 buckets.
	if len(env.Data.Buckets) != 28 {
		t.Errorf("expected 28 buckets for 7d/6h window, got %d", len(env.Data.Buckets))
	}
}

func TestTimeSeriesHandlerInvalidParams(t *testing.T) {
	store := newPolicyHandlerStore(t)
	h := api.NewTimeSeriesHandler(store)

	t.Run("invalid window", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window=99h", nil)
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("invalid bucket", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window=24h&bucket=2h", nil)
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
	})
}

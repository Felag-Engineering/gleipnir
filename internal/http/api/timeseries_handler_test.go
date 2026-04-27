package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
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

	// 24h window with 1h buckets produces 24 or 25 buckets depending on whether
	// now falls exactly on an hour boundary (ceil of window/step).
	n := len(env.Data.Buckets)
	if n < 24 || n > 25 {
		t.Errorf("expected 24 or 25 buckets for 24h/1h window, got %d", n)
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
		 VALUES ('r-s46', 'p1', 'claude-sonnet-4-6', 'complete', 'webhook', '{}', ?, ?, 1000)`,
		recent, recent,
	)
	store.DB().Exec(
		`INSERT INTO runs(id, policy_id, model, status, trigger_type, trigger_payload, started_at, created_at, token_cost)
		 VALUES ('r-h35', 'p1', 'claude-haiku-3-5-20241022', 'complete', 'webhook', '{}', ?, ?, 500)`,
		recent, recent,
	)
	store.DB().Exec(
		`INSERT INTO runs(id, policy_id, model, status, trigger_type, trigger_payload, started_at, created_at, token_cost)
		 VALUES ('r-h45', 'p1', 'claude-haiku-4-5', 'complete', 'webhook', '{}', ?, ?, 2200)`,
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

	// Keys should use display names, not raw API model IDs.
	if _, ok := merged["claude-sonnet-4-6"]; ok {
		t.Error("expected display name 'Sonnet 4.6', got raw API ID 'claude-sonnet-4-6'")
	}
	// claude-sonnet-4-6 now maps to "Sonnet 4.6" (not "Sonnet 4"; that is the dated alias).
	if merged["Sonnet 4.6"] != 1000 {
		t.Errorf("Sonnet 4.6 tokens = %d, want 1000", merged["Sonnet 4.6"])
	}
	if merged["Haiku 3.5"] != 500 {
		t.Errorf("Haiku 3.5 tokens = %d, want 500", merged["Haiku 3.5"])
	}
	if merged["Haiku 4.5"] != 2200 {
		t.Errorf("Haiku 4.5 tokens = %d, want 2200", merged["Haiku 4.5"])
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

	// 7d / 6h = 28 buckets when now is step-aligned, 29 when it is not.
	n := len(env.Data.Buckets)
	if n < 28 || n > 29 {
		t.Errorf("expected 28 or 29 buckets for 7d/6h window, got %d", n)
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

	t.Run("invalid bucket 10m", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window=24h&bucket=10m", nil)
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for bucket=10m, got %d", rec.Code)
		}
	})
}

// TestTimeSeriesHandlerDefaultBucketPerWindow verifies that each window uses the
// expected default bucket granularity when no bucket param is supplied.
func TestTimeSeriesHandlerDefaultBucketPerWindow(t *testing.T) {
	cases := []struct {
		window   string
		minCount int
		maxCount int
	}{
		// 24h at 5m (300s): 24*60/5 = 288; allow 288 or 289
		{"24h", 288, 289},
		// 7d at 6h: 7*24/6 = 28; allow 28 or 29
		{"7d", 28, 29},
		// 30d at 1d: 30; allow 30 or 31
		{"30d", 30, 31},
	}

	for _, tc := range cases {
		t.Run("window="+tc.window, func(t *testing.T) {
			store := newPolicyHandlerStore(t)
			h := api.NewTimeSeriesHandler(store)

			req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window="+tc.window, nil)
			rec := httptest.NewRecorder()
			h.Get(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}

			var env struct {
				Data api.TimeSeriesResponse `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
				t.Fatalf("decode: %v", err)
			}

			n := len(env.Data.Buckets)
			if n < tc.minCount || n > tc.maxCount {
				t.Errorf("window=%s: expected %d-%d buckets, got %d", tc.window, tc.minCount, tc.maxCount, n)
			}
		})
	}
}

// TestTimeSeriesHandlerRightEdgeTracksNow checks that the last bucket timestamp
// is within one step of now — confirming end=now rather than end=next-boundary.
func TestTimeSeriesHandlerRightEdgeTracksNow(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "pol1", "")

	// Insert a run created 1 minute ago.
	createdAt := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339Nano)
	testutil.InsertRunWithTime(t, store, "r1", "p1", model.RunStatusComplete, createdAt, 0)

	h := api.NewTimeSeriesHandler(store)
	before := time.Now().UTC()
	req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window=24h&bucket=5m", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	after := time.Now().UTC()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Data api.TimeSeriesResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Buckets) == 0 {
		t.Fatal("expected at least one bucket")
	}

	last := env.Data.Buckets[len(env.Data.Buckets)-1]
	lastTS, err := time.Parse(time.RFC3339, last.Timestamp)
	if err != nil {
		t.Fatalf("parse last bucket timestamp %q: %v", last.Timestamp, err)
	}

	step := 5 * time.Minute
	// The last bucket's timestamp must be <= now and > now - step.
	if lastTS.After(after) {
		t.Errorf("last bucket timestamp %v is after now %v", lastTS, after)
	}
	if !lastTS.After(before.Add(-step)) {
		t.Errorf("last bucket timestamp %v is more than one step (%v) before now %v", lastTS, step, before)
	}
}

// TestTimeSeriesHandlerFineBucket5m inserts three runs at -10m, -20m, and -30m
// and verifies they fall into three distinct 5-minute buckets.
func TestTimeSeriesHandlerFineBucket5m(t *testing.T) {
	store := newPolicyHandlerStore(t)
	insertTestPolicy(t, store, "p1", "pol1", "")

	now := time.Now().UTC()
	for i, offset := range []time.Duration{10, 20, 30} {
		ts := now.Add(-offset * time.Minute).Format(time.RFC3339Nano)
		testutil.InsertRunWithTime(t, store, fmt.Sprintf("r%d", i), "p1", model.RunStatusComplete, ts, 0)
	}

	h := api.NewTimeSeriesHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/stats/timeseries?window=24h&bucket=5m", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Data api.TimeSeriesResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Count how many buckets have exactly one completed run.
	singleRunBuckets := 0
	for _, b := range env.Data.Buckets {
		if b.Completed == 1 {
			singleRunBuckets++
		}
	}

	if singleRunBuckets != 3 {
		t.Errorf("expected 3 buckets each with one completed run (runs at -10m, -20m, -30m should be in separate 5m buckets), got %d", singleRunBuckets)
	}
}

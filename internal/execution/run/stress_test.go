package run_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// TestListSteps_10kStress seeds a run with 10k steps and verifies that the
// paginated endpoint responds quickly and that walking all pages produces
// exactly 10k steps in strictly ascending order.
//
// Skip in -short mode: this test does real DB I/O and takes several seconds.
func TestListSteps_10kStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test: skipping in -short mode")
	}

	const totalSteps = 10_000
	const pageSize = 500 // matches defaultStepsLimit in runs_handler.go

	store := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, store, "p-stress", "policy-stress", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, "r-stress", "p-stress", model.RunStatusComplete)

	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	for i := 0; i < totalSteps; i++ {
		_, err := store.CreateRunStep(ctx, db.CreateRunStepParams{
			ID:         fmt.Sprintf("step-%d", i),
			RunID:      "r-stress",
			StepNumber: int64(i),
			Type:       "thought",
			Content:    `{}`,
			TokenCost:  0,
			CreatedAt:  now,
		})
		if err != nil {
			t.Fatalf("CreateRunStep %d: %v", i, err)
		}
	}

	h := run.NewRunsHandler(store, run.NewRunManager(), nil)
	router := newRunsRouter(h)

	// First-page latency check. This is a catastrophic-regression smoke (e.g. a
	// missing index turning pagination into a full-table scan), NOT a precise
	// perf gate — an absolute wall-clock bound is hardware-sensitive (CLAUDE.md:
	// time-dependent tests) and this test runs under `-race` in CI, which slows
	// execution several-fold. The threshold is deliberately generous so loaded
	// amd64/arm64 runners cannot flake it while a true blow-up still trips it.
	const firstPageMax = 5 * time.Second
	firstPageStart := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r-stress/steps", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	elapsed := time.Since(firstPageStart)

	if w.Code != http.StatusOK {
		t.Fatalf("first page: status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if elapsed > firstPageMax {
		t.Errorf("first page took %s, want < %s", elapsed, firstPageMax)
	}

	// Walk every page and assert ordering + total count.
	var (
		seenTotal int64
		lastSeen  int64 = -1
		walkStart       = time.Now()
	)

	for {
		url := fmt.Sprintf("/api/v1/runs/r-stress/steps?after=%d&limit=%d", lastSeen, pageSize)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("page walk: status = %d, want 200", w.Code)
		}

		var env struct {
			Data []run.StepSummary `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
			t.Fatalf("page walk: decode response: %v", err)
		}

		for _, s := range env.Data {
			if s.StepNumber <= lastSeen {
				t.Errorf("step_number not strictly increasing: got %d after %d", s.StepNumber, lastSeen)
			}
			lastSeen = s.StepNumber
		}
		seenTotal += int64(len(env.Data))

		// Fewer rows than the page limit means we've reached the last page.
		if len(env.Data) < pageSize {
			break
		}
	}

	if seenTotal != totalSteps {
		t.Errorf("total steps walked = %d, want %d", seenTotal, totalSteps)
	}

	// Generous catastrophic-regression bound (see firstPageMax above): walking
	// 20 pages of 500 rows under `-race` on a loaded CI runner must stay well
	// under this, while a pathological regression (O(n²) paging) would exceed it.
	const fullWalkMax = 60 * time.Second
	walkElapsed := time.Since(walkStart)
	if walkElapsed > fullWalkMax {
		t.Errorf("full walk took %s, want < %s", walkElapsed, fullWalkMax)
	}
}

package trigger_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// insertTestStep adds a run_step directly via CreateRunStep.
func insertTestStep(t *testing.T, store *db.Store, stepID, runID string, stepNumber int64) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.CreateRunStep(context.Background(), db.CreateRunStepParams{
		ID:         stepID,
		RunID:      runID,
		StepNumber: stepNumber,
		Type:       "thought",
		Content:    "step content",
		TokenCost:  0,
		CreatedAt:  now,
	})
	if err != nil {
		t.Fatalf("insertTestStep %s: %v", stepID, err)
	}
}

func newRunsRouter(h *trigger.RunsHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/v1/runs", h.List)
	r.Get("/api/v1/runs/{runID}", h.Get)
	r.Get("/api/v1/runs/{runID}/steps", h.ListSteps)
	return r
}

func TestRunsHandler_List(t *testing.T) {
	cases := []struct {
		name             string
		setup            func(t *testing.T, store *db.Store)
		query            string
		wantCount        int
		wantCode         int
		wantBodyContains string
	}{
		{
			name: "no filters returns all runs",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-list-1", minimalWebhookPolicy)
				insertTestPolicy(t, store, "p-list-2", minimalWebhookPolicy)
				insertTestRun(t, store, "r-list-a", "p-list-1", model.RunStatusComplete)
				insertTestRun(t, store, "r-list-b", "p-list-2", model.RunStatusFailed)
			},
			query:     "",
			wantCount: 2,
			wantCode:  http.StatusOK,
		},
		{
			name: "filter by policy_id returns only matching runs",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-filter-pol", minimalWebhookPolicy)
				insertTestPolicy(t, store, "p-other-pol", minimalWebhookPolicy)
				insertTestRun(t, store, "r-filter-pol-1", "p-filter-pol", model.RunStatusComplete)
				insertTestRun(t, store, "r-filter-pol-2", "p-filter-pol", model.RunStatusFailed)
				insertTestRun(t, store, "r-other-pol-1", "p-other-pol", model.RunStatusComplete)
			},
			query:     "?policy_id=p-filter-pol",
			wantCount: 2,
			wantCode:  http.StatusOK,
		},
		{
			name: "filter by status returns only matching runs",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-filter-status", minimalWebhookPolicy)
				insertTestRun(t, store, "r-filter-status-1", "p-filter-status", model.RunStatusComplete)
				insertTestRun(t, store, "r-filter-status-2", "p-filter-status", model.RunStatusComplete)
				insertTestRun(t, store, "r-filter-status-3", "p-filter-status", model.RunStatusFailed)
			},
			query:     "?status=complete",
			wantCount: 2,
			wantCode:  http.StatusOK,
		},
		{
			name: "combined filter returns intersection",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-combined", minimalWebhookPolicy)
				insertTestPolicy(t, store, "p-combined-other", minimalWebhookPolicy)
				insertTestRun(t, store, "r-combined-1", "p-combined", model.RunStatusComplete)
				insertTestRun(t, store, "r-combined-2", "p-combined", model.RunStatusFailed)
				insertTestRun(t, store, "r-combined-3", "p-combined-other", model.RunStatusComplete)
			},
			query:     "?policy_id=p-combined&status=complete",
			wantCount: 1,
			wantCode:  http.StatusOK,
		},
		{
			name: "no matching runs returns empty array not 404",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-empty", minimalWebhookPolicy)
			},
			query:     "?policy_id=p-empty",
			wantCount: 0,
			wantCode:  http.StatusOK,
		},
		{
			name:             "invalid status returns 400",
			setup:            func(t *testing.T, store *db.Store) {},
			query:            "?status=banana",
			wantCount:        -1, // not decoded on non-200
			wantCode:         http.StatusBadRequest,
			wantBodyContains: "invalid status",
		},
		{
			name: "limit=2 with 3 runs returns 2 results",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-limit2", minimalWebhookPolicy)
				insertTestRun(t, store, "r-limit2-1", "p-limit2", model.RunStatusComplete)
				insertTestRun(t, store, "r-limit2-2", "p-limit2", model.RunStatusComplete)
				insertTestRun(t, store, "r-limit2-3", "p-limit2", model.RunStatusComplete)
			},
			query:     "?policy_id=p-limit2&limit=2",
			wantCount: 2,
			wantCode:  http.StatusOK,
		},
		{
			name: "limit=0 clamped to default returns all seeded runs",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-limit0", minimalWebhookPolicy)
				insertTestRun(t, store, "r-limit0-1", "p-limit0", model.RunStatusComplete)
				insertTestRun(t, store, "r-limit0-2", "p-limit0", model.RunStatusComplete)
			},
			query:     "?policy_id=p-limit0&limit=0",
			wantCount: 2,
			wantCode:  http.StatusOK,
		},
		{
			name: "limit=999 clamped to 200 returns 200 status",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-limit999", minimalWebhookPolicy)
				insertTestRun(t, store, "r-limit999-1", "p-limit999", model.RunStatusComplete)
			},
			query:     "?policy_id=p-limit999&limit=999",
			wantCount: 1,
			wantCode:  http.StatusOK,
		},
		{
			name: "offset=1 with 2 runs returns 1 result",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-offset1", minimalWebhookPolicy)
				insertTestRun(t, store, "r-offset1-a", "p-offset1", model.RunStatusComplete)
				insertTestRun(t, store, "r-offset1-b", "p-offset1", model.RunStatusComplete)
			},
			query:     "?policy_id=p-offset1&offset=1",
			wantCount: 1,
			wantCode:  http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			h := trigger.NewRunsHandler(store)
			router := newRunsRouter(h)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs"+tc.query, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantCode, w.Body.String())
			}
			if tc.wantBodyContains != "" && !strings.Contains(w.Body.String(), tc.wantBodyContains) {
				t.Errorf("body = %q, want it to contain %q", w.Body.String(), tc.wantBodyContains)
			}

			if tc.wantCode == http.StatusOK {
				ct := w.Result().Header.Get("Content-Type")
				if !strings.Contains(ct, "application/json") {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}

				var got []trigger.RunSummary
				if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if len(got) != tc.wantCount {
					t.Errorf("len(runs) = %d, want %d", len(got), tc.wantCount)
				}
			}
		})
	}
}

func TestRunsHandler_Get(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, store *db.Store)
		runID    string
		wantCode int
		// checkFn is called on the decoded RunSummary when wantCode == 200
		checkFn func(t *testing.T, run trigger.RunSummary)
	}{
		{
			name: "known ID returns 200 with correct fields",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-get", minimalWebhookPolicy)
				insertTestRun(t, store, "r-get-known", "p-get", model.RunStatusComplete)
			},
			runID:    "r-get-known",
			wantCode: http.StatusOK,
			checkFn: func(t *testing.T, run trigger.RunSummary) {
				if run.ID != "r-get-known" {
					t.Errorf("run.ID = %q, want %q", run.ID, "r-get-known")
				}
				if run.PolicyID != "p-get" {
					t.Errorf("run.PolicyID = %q, want %q", run.PolicyID, "p-get")
				}
				if run.Status != string(model.RunStatusComplete) {
					t.Errorf("run.Status = %q, want %q", run.Status, model.RunStatusComplete)
				}
				if run.TriggerType != "webhook" {
					t.Errorf("run.TriggerType = %q, want %q", run.TriggerType, "webhook")
				}
			},
		},
		{
			name:     "unknown ID returns 404",
			runID:    "r-does-not-exist",
			wantCode: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			h := trigger.NewRunsHandler(store)
			router := newRunsRouter(h)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+tc.runID, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantCode, w.Body.String())
			}

			if tc.checkFn != nil {
				ct := w.Result().Header.Get("Content-Type")
				if !strings.Contains(ct, "application/json") {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}

				var run trigger.RunSummary
				if err := json.NewDecoder(w.Body).Decode(&run); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				tc.checkFn(t, run)
			}
		})
	}
}

func TestRunsHandler_ListSteps(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T, store *db.Store)
		runID     string
		wantCode  int
		wantCount int
		// checkFn is called when wantCode == 200 and wantCount > 0
		checkFn func(t *testing.T, steps []trigger.StepSummary)
	}{
		{
			name: "run with steps returns 200 and steps in ascending order",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-steps", minimalWebhookPolicy)
				insertTestRun(t, store, "r-steps", "p-steps", model.RunStatusComplete)
				insertTestStep(t, store, "s-steps-1", "r-steps", 1)
				insertTestStep(t, store, "s-steps-2", "r-steps", 2)
				insertTestStep(t, store, "s-steps-3", "r-steps", 3)
			},
			runID:     "r-steps",
			wantCode:  http.StatusOK,
			wantCount: 3,
			checkFn: func(t *testing.T, steps []trigger.StepSummary) {
				for i, s := range steps {
					if s.StepNumber != int64(i+1) {
						t.Errorf("steps[%d].StepNumber = %d, want %d", i, s.StepNumber, i+1)
					}
				}
			},
		},
		{
			name: "run with no steps returns 200 and empty array",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-no-steps", minimalWebhookPolicy)
				insertTestRun(t, store, "r-no-steps", "p-no-steps", model.RunStatusPending)
			},
			runID:     "r-no-steps",
			wantCode:  http.StatusOK,
			wantCount: 0,
		},
		{
			name:     "unknown run ID returns 404",
			runID:    "r-steps-nonexistent",
			wantCode: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			h := trigger.NewRunsHandler(store)
			router := newRunsRouter(h)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+tc.runID+"/steps", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantCode, w.Body.String())
			}

			if tc.wantCode == http.StatusOK {
				ct := w.Result().Header.Get("Content-Type")
				if !strings.Contains(ct, "application/json") {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}

				var steps []trigger.StepSummary
				if err := json.NewDecoder(w.Body).Decode(&steps); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if len(steps) != tc.wantCount {
					t.Errorf("len(steps) = %d, want %d", len(steps), tc.wantCount)
				}
				if tc.checkFn != nil {
					tc.checkFn(t, steps)
				}
			}
		})
	}
}

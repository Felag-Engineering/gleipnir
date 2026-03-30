package trigger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
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
	r.Post("/api/v1/runs/{runID}/cancel", h.Cancel)
	r.Post("/api/v1/runs/{runID}/approval", h.SubmitApproval)
	r.Post("/api/v1/runs/{runID}/feedback", h.SubmitFeedback)
	return r
}

func TestRunsHandler_List(t *testing.T) {
	cases := []struct {
		name             string
		setup            func(t *testing.T, store *db.Store)
		query            string
		wantCount        int
		wantTotal        int64
		wantCode         int
		wantBodyContains string
		checkFn          func(t *testing.T, resp trigger.PaginatedRunsResponse)
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
			wantTotal: 2,
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
			wantTotal: 2,
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
			wantTotal: 2,
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
			wantTotal: 1,
			wantCode:  http.StatusOK,
		},
		{
			name: "no matching runs returns empty array not 404",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-empty", minimalWebhookPolicy)
			},
			query:     "?policy_id=p-empty",
			wantCount: 0,
			wantTotal: 0,
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
			name: "limit=2 with 3 runs returns 2 results but total=3",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-limit2", minimalWebhookPolicy)
				insertTestRun(t, store, "r-limit2-1", "p-limit2", model.RunStatusComplete)
				insertTestRun(t, store, "r-limit2-2", "p-limit2", model.RunStatusComplete)
				insertTestRun(t, store, "r-limit2-3", "p-limit2", model.RunStatusComplete)
			},
			query:     "?policy_id=p-limit2&limit=2",
			wantCount: 2,
			wantTotal: 3,
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
			wantTotal: 2,
			wantCode:  http.StatusOK,
		},
		{
			name: "limit=999 clamped to 100 returns 200 status",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-limit999", minimalWebhookPolicy)
				insertTestRun(t, store, "r-limit999-1", "p-limit999", model.RunStatusComplete)
			},
			query:     "?policy_id=p-limit999&limit=999",
			wantCount: 1,
			wantTotal: 1,
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
			wantTotal: 2,
			wantCode:  http.StatusOK,
		},
		{
			name: "filter by since returns only recent runs",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-since", minimalWebhookPolicy)
				old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
				testutil.InsertRunWithTime(t, store, "r-since-old", "p-since", model.RunStatusComplete, old, 0)
				recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
				testutil.InsertRunWithTime(t, store, "r-since-new", "p-since", model.RunStatusComplete, recent, 0)
			},
			query:     "?policy_id=p-since&since=" + time.Now().Add(-6*time.Hour).UTC().Format(time.RFC3339),
			wantCount: 1,
			wantTotal: 1,
			wantCode:  http.StatusOK,
		},
		{
			name: "filter by until returns only older runs",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-until", minimalWebhookPolicy)
				old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
				testutil.InsertRunWithTime(t, store, "r-until-old", "p-until", model.RunStatusComplete, old, 0)
				recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
				testutil.InsertRunWithTime(t, store, "r-until-new", "p-until", model.RunStatusComplete, recent, 0)
			},
			query:     "?policy_id=p-until&until=" + time.Now().Add(-6*time.Hour).UTC().Format(time.RFC3339),
			wantCount: 1,
			wantTotal: 1,
			wantCode:  http.StatusOK,
		},
		{
			name:             "invalid since returns 400",
			setup:            func(t *testing.T, store *db.Store) {},
			query:            "?since=not-a-time",
			wantCount:        -1,
			wantCode:         http.StatusBadRequest,
			wantBodyContains: "invalid since",
		},
		{
			name: "order=asc returns runs in ascending created_at order",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-asc", minimalWebhookPolicy)
				t1 := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
				t2 := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
				testutil.InsertRunWithTime(t, store, "r-asc-older", "p-asc", model.RunStatusComplete, t1, 0)
				testutil.InsertRunWithTime(t, store, "r-asc-newer", "p-asc", model.RunStatusComplete, t2, 0)
			},
			query:     "?policy_id=p-asc&order=asc",
			wantCount: 2,
			wantTotal: 2,
			wantCode:  http.StatusOK,
			checkFn: func(t *testing.T, resp trigger.PaginatedRunsResponse) {
				if len(resp.Runs) != 2 {
					t.Fatalf("expected 2 runs, got %d", len(resp.Runs))
				}
				if resp.Runs[0].ID != "r-asc-older" {
					t.Errorf("first run = %q, want %q", resp.Runs[0].ID, "r-asc-older")
				}
				if resp.Runs[1].ID != "r-asc-newer" {
					t.Errorf("second run = %q, want %q", resp.Runs[1].ID, "r-asc-newer")
				}
			},
		},
		{
			name:             "invalid sort returns 400 with list of valid values",
			setup:            func(t *testing.T, store *db.Store) {},
			query:            "?sort=tokens",
			wantCount:        -1,
			wantCode:         http.StatusBadRequest,
			wantBodyContains: "invalid sort",
		},
		{
			name: "sort=started_at is accepted as canonical alias",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-sort-started-at", minimalWebhookPolicy)
				t1 := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
				t2 := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
				testutil.InsertRunWithTime(t, store, "r-sort-started-at-older", "p-sort-started-at", model.RunStatusComplete, t1, 0)
				testutil.InsertRunWithTime(t, store, "r-sort-started-at-newer", "p-sort-started-at", model.RunStatusComplete, t2, 0)
			},
			query:     "?policy_id=p-sort-started-at&sort=started_at&order=asc",
			wantCount: 2,
			wantTotal: 2,
			wantCode:  http.StatusOK,
			checkFn: func(t *testing.T, resp trigger.PaginatedRunsResponse) {
				if len(resp.Runs) != 2 {
					t.Fatalf("expected 2 runs, got %d", len(resp.Runs))
				}
				if resp.Runs[0].ID != "r-sort-started-at-older" {
					t.Errorf("first run = %q, want %q", resp.Runs[0].ID, "r-sort-started-at-older")
				}
			},
		},
		{
			name: "sort=token_cost desc returns runs with highest token_cost first",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-sort-tc-desc", minimalWebhookPolicy)
				now := time.Now().UTC().Format(time.RFC3339)
				testutil.InsertRunWithTime(t, store, "r-sort-tc-low", "p-sort-tc-desc", model.RunStatusComplete, now, 10)
				testutil.InsertRunWithTime(t, store, "r-sort-tc-high", "p-sort-tc-desc", model.RunStatusComplete, now, 100)
			},
			query:     "?policy_id=p-sort-tc-desc&sort=token_cost",
			wantCount: 2,
			wantTotal: 2,
			wantCode:  http.StatusOK,
			checkFn: func(t *testing.T, resp trigger.PaginatedRunsResponse) {
				if len(resp.Runs) != 2 {
					t.Fatalf("expected 2 runs, got %d", len(resp.Runs))
				}
				if resp.Runs[0].ID != "r-sort-tc-high" {
					t.Errorf("first run = %q, want %q", resp.Runs[0].ID, "r-sort-tc-high")
				}
				if resp.Runs[1].ID != "r-sort-tc-low" {
					t.Errorf("second run = %q, want %q", resp.Runs[1].ID, "r-sort-tc-low")
				}
			},
		},
		{
			name: "sort=token_cost asc returns runs with lowest token_cost first",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-sort-tc-asc", minimalWebhookPolicy)
				now := time.Now().UTC().Format(time.RFC3339)
				testutil.InsertRunWithTime(t, store, "r-sort-tc-asc-low", "p-sort-tc-asc", model.RunStatusComplete, now, 10)
				testutil.InsertRunWithTime(t, store, "r-sort-tc-asc-high", "p-sort-tc-asc", model.RunStatusComplete, now, 100)
			},
			query:     "?policy_id=p-sort-tc-asc&sort=token_cost&order=asc",
			wantCount: 2,
			wantTotal: 2,
			wantCode:  http.StatusOK,
			checkFn: func(t *testing.T, resp trigger.PaginatedRunsResponse) {
				if len(resp.Runs) != 2 {
					t.Fatalf("expected 2 runs, got %d", len(resp.Runs))
				}
				if resp.Runs[0].ID != "r-sort-tc-asc-low" {
					t.Errorf("first run = %q, want %q", resp.Runs[0].ID, "r-sort-tc-asc-low")
				}
				if resp.Runs[1].ID != "r-sort-tc-asc-high" {
					t.Errorf("second run = %q, want %q", resp.Runs[1].ID, "r-sort-tc-asc-high")
				}
			},
		},
		{
			name: "sort=duration desc returns completed runs with longest duration first",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-sort-dur-desc", minimalWebhookPolicy)
				base := time.Now().Add(-10 * time.Minute).UTC()
				// short run: started 5 min ago, completed 4 min ago (1 min duration)
				shortStart := base.Add(5 * time.Minute).Format(time.RFC3339)
				shortEnd := base.Add(6 * time.Minute).Format(time.RFC3339)
				// long run: started 10 min ago, completed 3 min ago (7 min duration)
				longStart := base.Format(time.RFC3339)
				longEnd := base.Add(7 * time.Minute).Format(time.RFC3339)
				store.DB().Exec(
					`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, completed_at, created_at)
					 VALUES (?, ?, ?, 'webhook', '{}', ?, ?, ?)`,
					"r-dur-short", "p-sort-dur-desc", string(model.RunStatusComplete), shortStart, shortEnd, shortStart,
				)
				store.DB().Exec(
					`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, completed_at, created_at)
					 VALUES (?, ?, ?, 'webhook', '{}', ?, ?, ?)`,
					"r-dur-long", "p-sort-dur-desc", string(model.RunStatusComplete), longStart, longEnd, longStart,
				)
			},
			query:     "?policy_id=p-sort-dur-desc&sort=duration",
			wantCount: 2,
			wantTotal: 2,
			wantCode:  http.StatusOK,
			checkFn: func(t *testing.T, resp trigger.PaginatedRunsResponse) {
				if len(resp.Runs) != 2 {
					t.Fatalf("expected 2 runs, got %d", len(resp.Runs))
				}
				if resp.Runs[0].ID != "r-dur-long" {
					t.Errorf("first run = %q, want %q (longest duration)", resp.Runs[0].ID, "r-dur-long")
				}
			},
		},
		{
			name: "sort=duration asc returns completed runs with shortest duration first",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-sort-dur-asc", minimalWebhookPolicy)
				base := time.Now().Add(-10 * time.Minute).UTC()
				shortStart := base.Add(5 * time.Minute).Format(time.RFC3339)
				shortEnd := base.Add(6 * time.Minute).Format(time.RFC3339)
				longStart := base.Format(time.RFC3339)
				longEnd := base.Add(7 * time.Minute).Format(time.RFC3339)
				store.DB().Exec(
					`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, completed_at, created_at)
					 VALUES (?, ?, ?, 'webhook', '{}', ?, ?, ?)`,
					"r-dur-asc-short", "p-sort-dur-asc", string(model.RunStatusComplete), shortStart, shortEnd, shortStart,
				)
				store.DB().Exec(
					`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, completed_at, created_at)
					 VALUES (?, ?, ?, 'webhook', '{}', ?, ?, ?)`,
					"r-dur-asc-long", "p-sort-dur-asc", string(model.RunStatusComplete), longStart, longEnd, longStart,
				)
			},
			query:     "?policy_id=p-sort-dur-asc&sort=duration&order=asc",
			wantCount: 2,
			wantTotal: 2,
			wantCode:  http.StatusOK,
			checkFn: func(t *testing.T, resp trigger.PaginatedRunsResponse) {
				if len(resp.Runs) != 2 {
					t.Fatalf("expected 2 runs, got %d", len(resp.Runs))
				}
				if resp.Runs[0].ID != "r-dur-asc-short" {
					t.Errorf("first run = %q, want %q (shortest duration)", resp.Runs[0].ID, "r-dur-asc-short")
				}
			},
		},
		{
			name: "default limit is 25",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-default-limit", minimalWebhookPolicy)
				now := time.Now().UTC().Format(time.RFC3339)
				for i := 0; i < 30; i++ {
					id := fmt.Sprintf("r-default-limit-%02d", i)
					testutil.InsertRunWithTime(t, store, id, "p-default-limit", model.RunStatusComplete, now, 0)
				}
			},
			query:     "?policy_id=p-default-limit",
			wantCount: 25,
			wantTotal: 30,
			wantCode:  http.StatusOK,
		},
		{
			name: "policy_name is populated in list results",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-name-check", minimalWebhookPolicy)
				insertTestRun(t, store, "r-name-check", "p-name-check", model.RunStatusComplete)
			},
			query:     "?policy_id=p-name-check",
			wantCount: 1,
			wantTotal: 1,
			wantCode:  http.StatusOK,
			checkFn: func(t *testing.T, resp trigger.PaginatedRunsResponse) {
				if len(resp.Runs) == 0 {
					t.Fatal("expected at least 1 run")
				}
				if resp.Runs[0].PolicyName == "" {
					t.Errorf("policy_name is empty, want non-empty")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			h := trigger.NewRunsHandler(store, trigger.NewRunManager(), nil)
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

				var env struct {
					Data trigger.PaginatedRunsResponse `json:"data"`
				}
				if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if len(env.Data.Runs) != tc.wantCount {
					t.Errorf("len(runs) = %d, want %d", len(env.Data.Runs), tc.wantCount)
				}
				if tc.wantTotal >= 0 && env.Data.Total != tc.wantTotal {
					t.Errorf("total = %d, want %d", env.Data.Total, tc.wantTotal)
				}
				if tc.checkFn != nil {
					tc.checkFn(t, env.Data)
				}
			}
		})
	}
}

func TestRunsHandler_List_PolicyName(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestPolicy(t, store, "p-pname-test", minimalWebhookPolicy)
	insertTestRun(t, store, "r-pname-test-1", "p-pname-test", model.RunStatusComplete)

	h := trigger.NewRunsHandler(store, trigger.NewRunManager(), nil)
	router := newRunsRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?policy_id=p-pname-test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var env struct {
		Data trigger.PaginatedRunsResponse `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(env.Data.Runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(env.Data.Runs))
	}
	// insertTestPolicy uses "policy-" + policyID as the name (see webhook_test.go)
	wantName := "policy-p-pname-test"
	if env.Data.Runs[0].PolicyName != wantName {
		t.Errorf("policy_name = %q, want %q", env.Data.Runs[0].PolicyName, wantName)
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
			name: "system_prompt is returned when set",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-get-prompt", minimalWebhookPolicy)
				insertTestRun(t, store, "r-get-prompt", "p-get-prompt", model.RunStatusComplete)
				_, err := store.DB().Exec(
					`UPDATE runs SET system_prompt = ? WHERE id = ?`,
					"You are a helpful agent.", "r-get-prompt",
				)
				if err != nil {
					t.Fatalf("set system_prompt: %v", err)
				}
			},
			runID:    "r-get-prompt",
			wantCode: http.StatusOK,
			checkFn: func(t *testing.T, run trigger.RunSummary) {
				if run.SystemPrompt == nil {
					t.Fatal("system_prompt is nil, want non-nil")
				}
				if *run.SystemPrompt != "You are a helpful agent." {
					t.Errorf("system_prompt = %q, want %q", *run.SystemPrompt, "You are a helpful agent.")
				}
			},
		},
		{
			name: "system_prompt is null for old runs",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-get-no-prompt", minimalWebhookPolicy)
				insertTestRun(t, store, "r-get-no-prompt", "p-get-no-prompt", model.RunStatusComplete)
			},
			runID:    "r-get-no-prompt",
			wantCode: http.StatusOK,
			checkFn: func(t *testing.T, run trigger.RunSummary) {
				if run.SystemPrompt != nil {
					t.Errorf("system_prompt = %q, want nil", *run.SystemPrompt)
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
			store := testutil.NewTestStore(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			h := trigger.NewRunsHandler(store, trigger.NewRunManager(), nil)
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

				var env struct {
					Data trigger.RunSummary `json:"data"`
				}
				if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				tc.checkFn(t, env.Data)
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
				insertTestStep(t, store, "s-steps-1", "r-steps", 0)
				insertTestStep(t, store, "s-steps-2", "r-steps", 1)
				insertTestStep(t, store, "s-steps-3", "r-steps", 2)
			},
			runID:     "r-steps",
			wantCode:  http.StatusOK,
			wantCount: 3,
			checkFn: func(t *testing.T, steps []trigger.StepSummary) {
				for i, s := range steps {
					if s.StepNumber != int64(i) {
						t.Errorf("steps[%d].StepNumber = %d, want %d", i, s.StepNumber, i)
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
			store := testutil.NewTestStore(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			h := trigger.NewRunsHandler(store, trigger.NewRunManager(), nil)
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

				var env struct {
					Data []trigger.StepSummary `json:"data"`
				}
				if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if len(env.Data) != tc.wantCount {
					t.Errorf("len(steps) = %d, want %d", len(env.Data), tc.wantCount)
				}
				if tc.checkFn != nil {
					tc.checkFn(t, env.Data)
				}
			}
		})
	}
}

func TestRunsHandler_Cancel(t *testing.T) {
	type cancelSuccessBody struct {
		Data map[string]string `json:"data"`
	}
	type cancelErrorBody struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}

	cases := []struct {
		name          string
		setup         func(t *testing.T, store *db.Store, manager *trigger.RunManager)
		runID         string
		wantCode      int
		checkSuccess  func(t *testing.T, body cancelSuccessBody)
		checkConflict func(t *testing.T, body cancelErrorBody)
	}{
		{
			name: "running run returns 202 with run_id",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) {
				insertTestPolicy(t, store, "p-cancel-run", minimalWebhookPolicy)
				insertTestRun(t, store, "r-cancel-running", "p-cancel-run", model.RunStatusRunning)
				manager.Register("r-cancel-running", func() {}, make(chan bool), make(chan string))
			},
			runID:    "r-cancel-running",
			wantCode: http.StatusAccepted,
			checkSuccess: func(t *testing.T, body cancelSuccessBody) {
				if body.Data["run_id"] != "r-cancel-running" {
					t.Errorf("run_id = %q, want %q", body.Data["run_id"], "r-cancel-running")
				}
			},
		},
		{
			name:     "unknown run ID returns 404",
			setup:    func(t *testing.T, store *db.Store, manager *trigger.RunManager) {},
			runID:    "r-cancel-nonexistent",
			wantCode: http.StatusNotFound,
		},
		{
			name: "complete run returns 409 with error and status",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) {
				insertTestPolicy(t, store, "p-cancel-complete", minimalWebhookPolicy)
				insertTestRun(t, store, "r-cancel-complete", "p-cancel-complete", model.RunStatusComplete)
			},
			runID:    "r-cancel-complete",
			wantCode: http.StatusConflict,
			checkConflict: func(t *testing.T, body cancelErrorBody) {
				if body.Error != "run is not in a cancellable state" {
					t.Errorf("error = %q, want %q", body.Error, "run is not in a cancellable state")
				}
				if body.Detail != string(model.RunStatusComplete) {
					t.Errorf("detail = %q, want %q", body.Detail, model.RunStatusComplete)
				}
			},
		},
		{
			name: "pending run returns 409 with error and status",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) {
				insertTestPolicy(t, store, "p-cancel-pending", minimalWebhookPolicy)
				insertTestRun(t, store, "r-cancel-pending", "p-cancel-pending", model.RunStatusPending)
			},
			runID:    "r-cancel-pending",
			wantCode: http.StatusConflict,
			checkConflict: func(t *testing.T, body cancelErrorBody) {
				if body.Error != "run is not in a cancellable state" {
					t.Errorf("error = %q, want %q", body.Error, "run is not in a cancellable state")
				}
				if body.Detail != string(model.RunStatusPending) {
					t.Errorf("detail = %q, want %q", body.Detail, model.RunStatusPending)
				}
			},
		},
		{
			name: "failed run returns 409 with error and status",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) {
				insertTestPolicy(t, store, "p-cancel-failed", minimalWebhookPolicy)
				insertTestRun(t, store, "r-cancel-failed", "p-cancel-failed", model.RunStatusFailed)
			},
			runID:    "r-cancel-failed",
			wantCode: http.StatusConflict,
			checkConflict: func(t *testing.T, body cancelErrorBody) {
				if body.Error != "run is not in a cancellable state" {
					t.Errorf("error = %q, want %q", body.Error, "run is not in a cancellable state")
				}
				if body.Detail != string(model.RunStatusFailed) {
					t.Errorf("detail = %q, want %q", body.Detail, model.RunStatusFailed)
				}
			},
		},
		{
			name: "waiting_for_approval run returns 202",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) {
				insertTestPolicy(t, store, "p-cancel-waiting", minimalWebhookPolicy)
				insertTestRun(t, store, "r-cancel-waiting", "p-cancel-waiting", model.RunStatusWaitingForApproval)
				manager.Register("r-cancel-waiting", func() {}, make(chan bool), make(chan string))
			},
			runID:    "r-cancel-waiting",
			wantCode: http.StatusAccepted,
			checkSuccess: func(t *testing.T, body cancelSuccessBody) {
				if body.Data["run_id"] != "r-cancel-waiting" {
					t.Errorf("run_id = %q, want %q", body.Data["run_id"], "r-cancel-waiting")
				}
			},
		},
		{
			name: "waiting_for_feedback run returns 202",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) {
				insertTestPolicy(t, store, "p-cancel-feedback", minimalWebhookPolicy)
				insertTestRun(t, store, "r-cancel-feedback", "p-cancel-feedback", model.RunStatusWaitingForFeedback)
				manager.Register("r-cancel-feedback", func() {}, make(chan bool), make(chan string))
			},
			runID:    "r-cancel-feedback",
			wantCode: http.StatusAccepted,
			checkSuccess: func(t *testing.T, body cancelSuccessBody) {
				if body.Data["run_id"] != "r-cancel-feedback" {
					t.Errorf("run_id = %q, want %q", body.Data["run_id"], "r-cancel-feedback")
				}
			},
		},
		{
			name: "interrupted run returns 409 with error and status",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) {
				insertTestPolicy(t, store, "p-cancel-interrupted", minimalWebhookPolicy)
				insertTestRun(t, store, "r-cancel-interrupted", "p-cancel-interrupted", model.RunStatusInterrupted)
			},
			runID:    "r-cancel-interrupted",
			wantCode: http.StatusConflict,
			checkConflict: func(t *testing.T, body cancelErrorBody) {
				if body.Error != "run is not in a cancellable state" {
					t.Errorf("error = %q, want %q", body.Error, "run is not in a cancellable state")
				}
				if body.Detail != string(model.RunStatusInterrupted) {
					t.Errorf("detail = %q, want %q", body.Detail, model.RunStatusInterrupted)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			manager := trigger.NewRunManager()
			if tc.setup != nil {
				tc.setup(t, store, manager)
			}

			h := trigger.NewRunsHandler(store, manager, nil)
			router := newRunsRouter(h)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+tc.runID+"/cancel", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantCode, w.Body.String())
			}

			ct := w.Result().Header.Get("Content-Type")
			if tc.checkSuccess != nil || tc.checkConflict != nil {
				if !strings.Contains(ct, "application/json") {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
			}

			if tc.checkSuccess != nil {
				var body cancelSuccessBody
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatalf("decode success response: %v", err)
				}
				tc.checkSuccess(t, body)
			}

			if tc.checkConflict != nil {
				var body cancelErrorBody
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatalf("decode conflict response: %v", err)
				}
				tc.checkConflict(t, body)
			}
		})
	}
}

func TestRunsHandler_SubmitApproval(t *testing.T) {
	type successBody struct {
		Data map[string]string `json:"data"`
	}
	type approvalErrorBody struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}

	cases := []struct {
		name         string
		setup        func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan bool
		runID        string
		body         string
		wantCode     int
		checkSuccess func(t *testing.T, body successBody)
		checkError   func(t *testing.T, body approvalErrorBody)
	}{
		{
			name: "run not found returns 404",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan bool {
				return nil
			},
			runID:    "r-approval-missing",
			body:     `{"decision":"approved"}`,
			wantCode: http.StatusNotFound,
		},
		{
			name: "run not waiting_for_approval returns 409",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan bool {
				insertTestPolicy(t, store, "p-approval-running", minimalWebhookPolicy)
				insertTestRun(t, store, "r-approval-running", "p-approval-running", model.RunStatusRunning)
				return nil
			},
			runID:    "r-approval-running",
			body:     `{"decision":"approved"}`,
			wantCode: http.StatusConflict,
			checkError: func(t *testing.T, body approvalErrorBody) {
				if body.Error != "run is not waiting for approval" {
					t.Errorf("error = %q, want %q", body.Error, "run is not waiting for approval")
				}
				if body.Detail != string(model.RunStatusRunning) {
					t.Errorf("detail = %q, want %q", body.Detail, model.RunStatusRunning)
				}
			},
		},
		{
			name: "missing decision field returns 400",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan bool {
				return nil
			},
			runID:    "r-approval-bad-body",
			body:     `{}`,
			wantCode: http.StatusBadRequest,
			checkError: func(t *testing.T, body approvalErrorBody) {
				if body.Error == "" {
					t.Error("expected non-empty error message")
				}
			},
		},
		{
			name: "invalid decision value returns 400",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan bool {
				return nil
			},
			runID:    "r-approval-bad-decision",
			body:     `{"decision":"maybe"}`,
			wantCode: http.StatusBadRequest,
			checkError: func(t *testing.T, body approvalErrorBody) {
				if body.Error == "" {
					t.Error("expected non-empty error message")
				}
			},
		},
		{
			name: "invalid JSON body returns 400",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan bool {
				return nil
			},
			runID:    "r-approval-bad-json",
			body:     `not-json`,
			wantCode: http.StatusBadRequest,
		},
		{
			name: "waiting_for_approval but no active gate returns 409",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan bool {
				insertTestPolicy(t, store, "p-approval-no-gate", minimalWebhookPolicy)
				insertTestRun(t, store, "r-approval-no-gate", "p-approval-no-gate", model.RunStatusWaitingForApproval)
				// Run is registered but nobody is reading from the channel.
				manager.Register("r-approval-no-gate", func() {}, make(chan bool), make(chan string))
				return nil
			},
			runID:    "r-approval-no-gate",
			body:     `{"decision":"approved"}`,
			wantCode: http.StatusConflict,
			checkError: func(t *testing.T, body approvalErrorBody) {
				if body.Error != "no active approval gate for this run" {
					t.Errorf("error = %q, want %q", body.Error, "no active approval gate for this run")
				}
			},
		},
		{
			name: "approved decision delivered returns 202",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan bool {
				insertTestPolicy(t, store, "p-approval-ok", minimalWebhookPolicy)
				insertTestRun(t, store, "r-approval-ok", "p-approval-ok", model.RunStatusWaitingForApproval)
				testutil.InsertApprovalRequest(t, store, "ar-approval-ok", "r-approval-ok", "some_tool")
				// Buffered so the non-blocking send in SendApproval succeeds without
				// needing a goroutine to be scheduled and blocking on the channel.
				ch := make(chan bool, 1)
				manager.Register("r-approval-ok", func() {}, ch, make(chan string))
				return ch
			},
			runID:    "r-approval-ok",
			body:     `{"decision":"approved"}`,
			wantCode: http.StatusAccepted,
			checkSuccess: func(t *testing.T, body successBody) {
				if body.Data["run_id"] != "r-approval-ok" {
					t.Errorf("run_id = %q, want %q", body.Data["run_id"], "r-approval-ok")
				}
				if body.Data["decision"] != "approved" {
					t.Errorf("decision = %q, want %q", body.Data["decision"], "approved")
				}
			},
		},
		{
			name: "denied decision delivered returns 202",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan bool {
				insertTestPolicy(t, store, "p-approval-deny", minimalWebhookPolicy)
				insertTestRun(t, store, "r-approval-deny", "p-approval-deny", model.RunStatusWaitingForApproval)
				testutil.InsertApprovalRequest(t, store, "ar-approval-deny", "r-approval-deny", "some_tool")
				// Buffered so the non-blocking send in SendApproval succeeds without
				// needing a goroutine to be scheduled and blocking on the channel.
				ch := make(chan bool, 1)
				manager.Register("r-approval-deny", func() {}, ch, make(chan string))
				return ch
			},
			runID:    "r-approval-deny",
			body:     `{"decision":"denied"}`,
			wantCode: http.StatusAccepted,
			checkSuccess: func(t *testing.T, body successBody) {
				if body.Data["decision"] != "denied" {
					t.Errorf("decision = %q, want %q", body.Data["decision"], "denied")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			manager := trigger.NewRunManager()

			var approvalCh chan bool
			if tc.setup != nil {
				approvalCh = tc.setup(t, store, manager)
			}

			// Drain any buffered channel to avoid leaks.
			if approvalCh != nil {
				go func() { <-approvalCh }()
			}

			h := trigger.NewRunsHandler(store, manager, nil)
			router := newRunsRouter(h)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+tc.runID+"/approval", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantCode, w.Body.String())
			}

			if tc.checkSuccess != nil {
				var body successBody
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatalf("decode success response: %v", err)
				}
				tc.checkSuccess(t, body)
			}

			if tc.checkError != nil {
				var body approvalErrorBody
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				tc.checkError(t, body)
			}

			// Clean up any registered runs to drain the WaitGroup.
			manager.Deregister(tc.runID)
		})
	}
}

func TestRunsHandler_SubmitFeedback(t *testing.T) {
	type successBody struct {
		Data map[string]string `json:"data"`
	}
	type feedbackErrorBody struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}

	cases := []struct {
		name         string
		setup        func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan string
		runID        string
		body         string
		wantCode     int
		checkSuccess func(t *testing.T, body successBody)
		checkError   func(t *testing.T, body feedbackErrorBody)
	}{
		{
			name: "run not found returns 404",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan string {
				return nil
			},
			runID:    "r-feedback-missing",
			body:     `{"response":"yes proceed"}`,
			wantCode: http.StatusNotFound,
		},
		{
			name: "run not waiting_for_feedback returns 409",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan string {
				insertTestPolicy(t, store, "p-feedback-running", minimalWebhookPolicy)
				insertTestRun(t, store, "r-feedback-running", "p-feedback-running", model.RunStatusRunning)
				return nil
			},
			runID:    "r-feedback-running",
			body:     `{"response":"yes proceed"}`,
			wantCode: http.StatusConflict,
			checkError: func(t *testing.T, body feedbackErrorBody) {
				if body.Error != "run is not waiting for feedback" {
					t.Errorf("error = %q, want %q", body.Error, "run is not waiting for feedback")
				}
			},
		},
		{
			name: "empty response returns 400",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan string {
				return nil
			},
			runID:    "r-feedback-empty",
			body:     `{"response":""}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name: "waiting_for_feedback but no active gate returns 409",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan string {
				insertTestPolicy(t, store, "p-feedback-no-gate", minimalWebhookPolicy)
				insertTestRun(t, store, "r-feedback-no-gate", "p-feedback-no-gate", model.RunStatusWaitingForFeedback)
				// Run is registered but nobody is reading from the channel.
				manager.Register("r-feedback-no-gate", func() {}, make(chan bool), make(chan string))
				return nil
			},
			runID:    "r-feedback-no-gate",
			body:     `{"response":"yes proceed"}`,
			wantCode: http.StatusConflict,
			checkError: func(t *testing.T, body feedbackErrorBody) {
				if body.Error != "no active feedback gate for this run" {
					t.Errorf("error = %q, want %q", body.Error, "no active feedback gate for this run")
				}
			},
		},
		{
			name: "response delivered returns 202",
			setup: func(t *testing.T, store *db.Store, manager *trigger.RunManager) chan string {
				insertTestPolicy(t, store, "p-feedback-ok", minimalWebhookPolicy)
				insertTestRun(t, store, "r-feedback-ok", "p-feedback-ok", model.RunStatusWaitingForFeedback)
				// Buffered so the non-blocking send in SendFeedback succeeds.
				ch := make(chan string, 1)
				manager.Register("r-feedback-ok", func() {}, make(chan bool), ch)
				return ch
			},
			runID:    "r-feedback-ok",
			body:     `{"response":"yes, proceed with caution"}`,
			wantCode: http.StatusAccepted,
			checkSuccess: func(t *testing.T, body successBody) {
				if body.Data["run_id"] != "r-feedback-ok" {
					t.Errorf("run_id = %q, want %q", body.Data["run_id"], "r-feedback-ok")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			manager := trigger.NewRunManager()

			if tc.setup != nil {
				tc.setup(t, store, manager)
			}

			h := trigger.NewRunsHandler(store, manager, nil)
			router := newRunsRouter(h)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+tc.runID+"/feedback", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantCode, w.Body.String())
			}

			if tc.checkSuccess != nil {
				var body successBody
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatalf("decode success response: %v", err)
				}
				tc.checkSuccess(t, body)
			}

			if tc.checkError != nil {
				var body feedbackErrorBody
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				tc.checkError(t, body)
			}

			manager.Deregister(tc.runID)
		})
	}
}

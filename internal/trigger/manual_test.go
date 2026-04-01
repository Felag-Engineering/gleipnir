package trigger_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

const minimalManualPolicy = `
name: test-manual-policy
trigger:
  type: manual
agent:
  model: claude-opus-4-6
  task: "test task"
`

const parallelManualPolicy = `
name: test-manual-parallel-policy
trigger:
  type: manual
agent:
  model: claude-opus-4-6
  task: "test task"
  concurrency: parallel
`

const queueManualPolicy = `
name: test-manual-queue-policy
trigger:
  type: manual
agent:
  model: claude-opus-4-6
  task: "test task"
  concurrency: queue
`

const replaceManualPolicy = `
name: test-manual-replace-policy
trigger:
  type: manual
agent:
  model: claude-opus-4-6
  task: "test task"
  concurrency: replace
`

const queueManualPolicyDepth1 = `
name: test-manual-queue-depth1-policy
trigger:
  type: manual
agent:
  model: claude-opus-4-6
  task: "test task"
  concurrency: queue
  queue_depth: 1
`

func insertTestManualPolicy(t *testing.T, store *db.Store, policyID, yaml string) {
	t.Helper()
	testutil.InsertPolicy(t, store, policyID, "manual-policy-"+policyID, "manual", yaml)
}

// callManualHandler builds a chi router, registers the handler, and fires a request.
func callManualHandler(t *testing.T, h *trigger.ManualTriggerHandler, policyID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Use(api.BodySizeLimit(api.MaxRequestBodySize))
	r.Post("/api/v1/policies/{policyID}/trigger", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies/"+policyID+"/trigger", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestManualTriggerHandler(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T, store *db.Store)
		policyID   string
		body       string
		wantStatus int
	}{
		{
			name:       "404 for unknown policy",
			policyID:   "nonexistent-policy-id",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "400 for non-JSON body",
			body:       "not json",
			wantStatus: http.StatusBadRequest,
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-badjson", minimalManualPolicy)
			},
			policyID: "mp-badjson",
		},
		{
			name: "409 for skip with active run",
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-skip-active", minimalManualPolicy)
				insertTestRun(t, store, "mr-active", "mp-skip-active", model.RunStatusRunning)
			},
			policyID:   "mp-skip-active",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusConflict,
		},
		{
			name: "202 for skip with no active runs",
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-skip-empty", minimalManualPolicy)
			},
			policyID:   "mp-skip-empty",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "202 for parallel",
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-parallel", parallelManualPolicy)
			},
			policyID:   "mp-parallel",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "202 queued for queue with active run",
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-queue-active", queueManualPolicy)
				insertTestRun(t, store, "mr-queue-active", "mp-queue-active", model.RunStatusRunning)
			},
			policyID:   "mp-queue-active",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "202 launch for queue with no active run",
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-queue-empty", queueManualPolicy)
			},
			policyID:   "mp-queue-empty",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "429 for queue with full queue",
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-queue-full", queueManualPolicyDepth1)
				insertTestRun(t, store, "mr-queue-full", "mp-queue-full", model.RunStatusRunning)
				testutil.InsertQueueEntry(t, store, "mp-queue-full", "manual")
			},
			policyID:   "mp-queue-full",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name: "501 for replace",
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-replace", replaceManualPolicy)
			},
			policyID:   "mp-replace",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusNotImplemented,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			registry := mcp.NewRegistry(store.Queries())
			noopClient := testutil.NewNoopLLMClient()
			providerReg := llm.NewProviderRegistry()
			providerReg.Register("anthropic", noopClient)
			launcher := trigger.NewRunLauncher(store, registry, trigger.NewRunManager(), trigger.NewAgentFactory(providerReg, nil), nil, 0)
			h := trigger.NewManualTriggerHandler(store, launcher)

			w := callManualHandler(t, h, tc.policyID, tc.body)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestManualTriggerHandler_RunCreatedInDB(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestManualPolicy(t, store, "mp-run-created", minimalManualPolicy)

	registry := mcp.NewRegistry(store.Queries())
	noopClient := testutil.NewNoopLLMClient()
	providerReg := llm.NewProviderRegistry()
	providerReg.Register("anthropic", noopClient)
	launcher := trigger.NewRunLauncher(store, registry, trigger.NewRunManager(), trigger.NewAgentFactory(providerReg, nil), nil, 0)
	h := trigger.NewManualTriggerHandler(store, launcher)

	w := callManualHandler(t, h, "mp-run-created", `{"message": "test"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	// The run row is created synchronously before the goroutine launches,
	// so we can query immediately without waiting.
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "mp-run-created", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected at least one run in DB after 202 response, got 0")
	}
	run := runs[0]
	if run.PolicyID != "mp-run-created" {
		t.Errorf("run.PolicyID = %q, want %q", run.PolicyID, "mp-run-created")
	}
	if run.TriggerType != "manual" {
		t.Errorf("run.TriggerType = %q, want %q", run.TriggerType, "manual")
	}
}

func TestManualTriggerHandler_EmptyBody(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestManualPolicy(t, store, "mp-empty-body", minimalManualPolicy)

	registry := mcp.NewRegistry(store.Queries())
	noopClient := testutil.NewNoopLLMClient()
	providerReg := llm.NewProviderRegistry()
	providerReg.Register("anthropic", noopClient)
	launcher := trigger.NewRunLauncher(store, registry, trigger.NewRunManager(), trigger.NewAgentFactory(providerReg, nil), nil, 0)
	h := trigger.NewManualTriggerHandler(store, launcher)

	// Empty body should be accepted (treated as '{}')
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies/mp-empty-body/trigger", strings.NewReader(""))
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Use(api.BodySizeLimit(api.MaxRequestBodySize))
	r.Post("/api/v1/policies/{policyID}/trigger", h.Handle)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}
}

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

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/trigger"
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
	r.Use(httputil.BodySizeLimit(httputil.MaxRequestBodySize))
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
			name: "202 for replace with no active run",
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-replace", replaceManualPolicy)
			},
			policyID:   "mp-replace",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "409 when policy is paused",
			setup: func(t *testing.T, store *db.Store) {
				insertTestManualPolicy(t, store, "mp-paused", minimalManualPolicy)
				now := time.Now().UTC().Format(time.RFC3339Nano)
				if err := store.SetPolicyPausedAt(context.Background(), db.SetPolicyPausedAtParams{
					PausedAt: &now,
					ID:       "mp-paused",
				}); err != nil {
					t.Fatalf("SetPolicyPausedAt: %v", err)
				}
			},
			policyID:   "mp-paused",
			body:       `{"message": "test"}`,
			wantStatus: http.StatusConflict,
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
			resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
			launcher := run.NewRunLauncher(run.RunLauncherConfig{
				Store:                  store,
				Registry:               registry,
				Manager:                run.NewRunManager(),
				AgentFactory:           run.NewAgentFactory(providerReg),
				Publisher:              nil,
				DefaultFeedbackTimeout: 0,
				ModelResolver:          resolver,
			})
			h := trigger.NewManualTriggerHandler(store, launcher, resolver)

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
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                run.NewRunManager(),
		AgentFactory:           run.NewAgentFactory(providerReg),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
	h := trigger.NewManualTriggerHandler(store, launcher, resolver)

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

// Policy that grants a tool not registered with any MCP server. Tool
// resolution will fail and Launch should return an error after the run row
// has already been created and marked failed.
const policyWithMissingTool = `
name: missing-tool-policy
trigger:
  type: manual
capabilities:
  tools:
    - tool: ghost-server.nonexistent_tool
agent:
  model: claude-opus-4-6
  task: "test task"
`

// When tool resolution fails, the manual trigger handler must return a 500
// response that includes (a) the underlying error as `detail` so the operator
// can see *what* went wrong (e.g. a removed tool) and (b) the `run_id` of the
// failed run row so the UI can deep-link to it.
func TestManualTriggerHandler_LaunchFailureSurfacesDetailAndRunID(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestManualPolicy(t, store, "mp-missing-tool", policyWithMissingTool)

	// No MCP servers registered, so ghost-server.nonexistent_tool will fail
	// to resolve.
	registry := mcp.NewRegistry(store.Queries())
	noopClient := testutil.NewNoopLLMClient()
	providerReg := llm.NewProviderRegistry()
	providerReg.Register("anthropic", noopClient)
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                run.NewRunManager(),
		AgentFactory:           run.NewAgentFactory(providerReg),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
	h := trigger.NewManualTriggerHandler(store, launcher, resolver)

	w := callManualHandler(t, h, "mp-missing-tool", `{"message": "test"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	// Top-level error should be the generic launch-failure label.
	if body["error"] != "failed to launch run" {
		t.Errorf(`body.error = %q, want %q`, body["error"], "failed to launch run")
	}

	// Detail must surface the underlying tool-resolution error so the operator
	// can see exactly which tool is missing — that is the whole point of this
	// PR. A bare "failed to launch run" with empty detail is a regression.
	detail, ok := body["detail"].(string)
	if !ok || detail == "" {
		t.Fatalf("body.detail must be a non-empty string carrying the underlying error; got %v", body["detail"])
	}
	if !strings.Contains(detail, "nonexistent_tool") {
		t.Errorf("body.detail = %q, expected it to mention the missing tool name", detail)
	}

	// run_id must be populated and must point at a real failed run row so the
	// UI can deep-link to it.
	runID, ok := body["run_id"].(string)
	if !ok || runID == "" {
		t.Fatalf("body.run_id must be a non-empty string when the run row was created; got %v", body["run_id"])
	}
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "mp-missing-tool", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly 1 run row, got %d", len(runs))
	}
	if runs[0].ID != runID {
		t.Errorf("body.run_id = %q, want %q (the failed run's ID)", runID, runs[0].ID)
	}
	if runs[0].Status != string(model.RunStatusFailed) {
		t.Errorf("run.Status = %q, want %q", runs[0].Status, model.RunStatusFailed)
	}
}

func TestManualTriggerHandler_EmptyBody(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestManualPolicy(t, store, "mp-empty-body", minimalManualPolicy)

	registry := mcp.NewRegistry(store.Queries())
	noopClient := testutil.NewNoopLLMClient()
	providerReg := llm.NewProviderRegistry()
	providerReg.Register("anthropic", noopClient)
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                run.NewRunManager(),
		AgentFactory:           run.NewAgentFactory(providerReg),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
	h := trigger.NewManualTriggerHandler(store, launcher, resolver)

	// Empty body should be accepted (treated as '{}')
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies/mp-empty-body/trigger", strings.NewReader(""))
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Use(httputil.BodySizeLimit(httputil.MaxRequestBodySize))
	r.Post("/api/v1/policies/{policyID}/trigger", h.Handle)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}
}

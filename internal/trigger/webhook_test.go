package trigger_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// minimalWebhookPolicy is the smallest YAML that parses cleanly with trigger type webhook
// and the default concurrency (skip).
const minimalWebhookPolicy = `
name: test-policy
trigger:
  type: webhook
agent:
  model: claude-opus-4-5
  task: "test task"
`

const parallelPolicy = `
name: test-parallel-policy
trigger:
  type: webhook
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: parallel
`

const queuePolicy = `
name: test-queue-policy
trigger:
  type: webhook
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
`

const replacePolicy = `
name: test-replace-policy
trigger:
  type: webhook
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: replace
`

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func insertTestPolicy(t *testing.T, store *db.Store, policyID, yaml string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.CreatePolicy(context.Background(), db.CreatePolicyParams{
		ID:          policyID,
		Name:        "policy-" + policyID,
		TriggerType: "webhook",
		Yaml:        yaml,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("insertTestPolicy %s: %v", policyID, err)
	}
}

func insertTestRun(t *testing.T, store *db.Store, runID, policyID string, status model.RunStatus) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.DB().Exec(
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
		 VALUES (?, ?, ?, 'webhook', '{}', ?, ?)`,
		runID, policyID, string(status), now, now,
	)
	if err != nil {
		t.Fatalf("insertTestRun %s: %v", runID, err)
	}
}

// callHandler builds a chi router, registers the handler, and fires a request.
// Returns the recorded response.
func callHandler(t *testing.T, h *trigger.WebhookHandler, policyID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/api/v1/webhooks/{policyID}", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/"+policyID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestWebhookHandler(t *testing.T) {
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
			body:       `{"event": "test"}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "400 for non-JSON body",
			body:       "not json",
			wantStatus: http.StatusBadRequest,
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-badjson", minimalWebhookPolicy)
			},
			policyID: "p-badjson",
		},
		{
			name: "409 for skip with active run",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-skip-active", minimalWebhookPolicy)
				insertTestRun(t, store, "r-active", "p-skip-active", model.RunStatusRunning)
			},
			policyID:   "p-skip-active",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusConflict,
		},
		{
			name: "202 for skip with no active runs",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-skip-empty", minimalWebhookPolicy)
			},
			policyID:   "p-skip-empty",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "202 for parallel",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-parallel", parallelPolicy)
			},
			policyID:   "p-parallel",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "501 for queue",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-queue", queuePolicy)
			},
			policyID:   "p-queue",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusNotImplemented,
		},
		{
			name: "501 for replace",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-replace", replacePolicy)
			},
			policyID:   "p-replace",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusNotImplemented,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			registry := mcp.NewRegistry(store.DB())
			claudeClient := anthropic.NewClient()
			h := trigger.NewWebhookHandler(store, registry, &claudeClient)

			w := callHandler(t, h, tc.policyID, tc.body)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestWebhookHandler_RunCreatedInDB(t *testing.T) {
	store := newTestStore(t)
	insertTestPolicy(t, store, "p-run-created", minimalWebhookPolicy)

	registry := mcp.NewRegistry(store.DB())
	claudeClient := anthropic.NewClient()
	h := trigger.NewWebhookHandler(store, registry, &claudeClient)

	w := callHandler(t, h, "p-run-created", `{"event": "test"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	// Give the goroutine a moment to update the DB before we query.
	// The goroutine will fail quickly (no Claude API key), so the run will
	// transition to failed. We just need to verify the run was created.
	time.Sleep(100 * time.Millisecond)

	runs, err := store.ListRunsByPolicy(context.Background(), "p-run-created")
	if err != nil {
		t.Fatalf("ListRunsByPolicy: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected at least one run in DB after 202 response, got 0")
	}
	// The run should be in a known final or active state.
	run := runs[0]
	if run.PolicyID != "p-run-created" {
		t.Errorf("run.PolicyID = %q, want %q", run.PolicyID, "p-run-created")
	}
	if run.TriggerType != "webhook" {
		t.Errorf("run.TriggerType = %q, want %q", run.TriggerType, "webhook")
	}
}

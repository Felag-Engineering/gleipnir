package trigger_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
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

const queuePolicyDepth1 = `
name: test-queue-depth1-policy
trigger:
  type: webhook
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
  queue_depth: 1
`

const webhookPolicyWithSecret = `
name: test-secret-policy
trigger:
  type: webhook
  webhook_secret: "test-secret-key-must-be-at-least-32-bytes-long"
agent:
  model: claude-opus-4-5
  task: "test task"
`

// insertTestPolicy inserts a webhook policy with the given ID and YAML.
func insertTestPolicy(t *testing.T, store *db.Store, policyID, yaml string) {
	t.Helper()
	testutil.InsertPolicy(t, store, policyID, "policy-"+policyID, "webhook", yaml)
}

// insertTestRun inserts a run with the given IDs and status.
func insertTestRun(t *testing.T, store *db.Store, runID, policyID string, status model.RunStatus) {
	t.Helper()
	testutil.InsertRun(t, store, runID, policyID, status)
}

// callHandler builds a chi router, registers the handler, and fires a request.
// Returns the recorded response.
func callHandler(t *testing.T, h *trigger.WebhookHandler, policyID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callHandlerWithHeaders(t, h, policyID, body, nil)
}

// callHandlerWithHeaders builds a chi router, registers the handler, and fires a request
// with additional headers. Returns the recorded response.
func callHandlerWithHeaders(t *testing.T, h *trigger.WebhookHandler, policyID, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Use(api.BodySizeLimit(api.MaxRequestBodySize))
	r.Post("/api/v1/webhooks/{policyID}", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/"+policyID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// computeTestSignature returns the correct X-Gleipnir-Signature header value
// for the given secret and body.
func computeTestSignature(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookHandler(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T, store *db.Store)
		policyID   string
		body       string
		headers    map[string]string
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
			name: "400 for body exceeding 1 MiB limit",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-toolarge", minimalWebhookPolicy)
			},
			policyID: "p-toolarge",
			// BodySizeLimit middleware wraps the body with http.MaxBytesReader, so
			// io.ReadAll returns an error when the limit is exceeded.
			body:       "{\"x\":\"" + strings.Repeat("a", 1<<20) + "\"}",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "400 for empty body",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-emptybody", minimalWebhookPolicy)
			},
			policyID:   "p-emptybody",
			body:       "",
			wantStatus: http.StatusBadRequest,
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
			name: "202 queued for queue with active run",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-queue-active", queuePolicy)
				insertTestRun(t, store, "r-queue-active", "p-queue-active", model.RunStatusRunning)
			},
			policyID:   "p-queue-active",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "202 launch for queue with no active run",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-queue-empty", queuePolicy)
			},
			policyID:   "p-queue-empty",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "429 for queue with full queue",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-queue-full", queuePolicyDepth1)
				insertTestRun(t, store, "r-queue-full", "p-queue-full", model.RunStatusRunning)
				// Fill the queue to depth 1.
				testutil.InsertQueueEntry(t, store, "p-queue-full", "webhook")
			},
			policyID:   "p-queue-full",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name: "202 for replace with no active run",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-replace", replacePolicy)
			},
			policyID:   "p-replace",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "401 missing signature when secret configured",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-secret-missing-sig", webhookPolicyWithSecret)
			},
			policyID:   "p-secret-missing-sig",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "403 invalid signature when secret configured",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-secret-bad-sig", webhookPolicyWithSecret)
			},
			policyID: "p-secret-bad-sig",
			body:     `{"event": "test"}`,
			headers: map[string]string{
				"X-Gleipnir-Signature": "sha256=deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "202 valid signature when secret configured",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-secret-valid-sig", webhookPolicyWithSecret)
			},
			policyID: "p-secret-valid-sig",
			body:     `{"event": "test"}`,
			headers: map[string]string{
				"X-Gleipnir-Signature": computeTestSignature(
					"test-secret-key-must-be-at-least-32-bytes-long",
					`{"event": "test"}`,
				),
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name: "202 no secret configured (open webhook)",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-no-secret", minimalWebhookPolicy)
			},
			policyID:   "p-no-secret",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusAccepted,
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
			launcher := trigger.NewRunLauncher(store, registry, trigger.NewRunManager(), trigger.NewAgentFactory(providerReg), nil, 0)
			h := trigger.NewWebhookHandler(store, launcher)

			w := callHandlerWithHeaders(t, h, tc.policyID, tc.body, tc.headers)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestWebhookHandler_RunCreatedInDB(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestPolicy(t, store, "p-run-created", minimalWebhookPolicy)

	registry := mcp.NewRegistry(store.Queries())
	noopClient := testutil.NewNoopLLMClient()
	providerReg := llm.NewProviderRegistry()
	providerReg.Register("anthropic", noopClient)
	launcher := trigger.NewRunLauncher(store, registry, trigger.NewRunManager(), trigger.NewAgentFactory(providerReg), nil, 0)
	h := trigger.NewWebhookHandler(store, launcher)

	w := callHandler(t, h, "p-run-created", `{"event": "test"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var runs []db.Run
	deadline := time.Now().Add(3 * time.Second)
	for {
		var err error
		runs, err = store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-run-created", Limit: 100})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for run to appear in DB")
		}
		time.Sleep(10 * time.Millisecond)
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

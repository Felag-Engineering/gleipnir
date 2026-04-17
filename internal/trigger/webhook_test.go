package trigger_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/httputil"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/run"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

const parallelPolicy = `
name: test-parallel-policy
trigger:
  type: webhook
  auth: none
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: parallel
`

const queuePolicy = `
name: test-queue-policy
trigger:
  type: webhook
  auth: none
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
`

const replacePolicy = `
name: test-replace-policy
trigger:
  type: webhook
  auth: none
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: replace
`

const queuePolicyDepth1 = `
name: test-queue-depth1-policy
trigger:
  type: webhook
  auth: none
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
  queue_depth: 1
`

// webhookPolicyHMAC has auth: hmac — secrets are loaded from the DB, not YAML.
const webhookPolicyHMAC = `
name: test-hmac-policy
trigger:
  type: webhook
  auth: hmac
agent:
  model: claude-opus-4-5
  task: "test task"
`

// webhookPolicyBearer has auth: bearer.
const webhookPolicyBearer = `
name: test-bearer-policy
trigger:
  type: webhook
  auth: bearer
agent:
  model: claude-opus-4-5
  task: "test task"
`

// webhookPolicyNone has auth: none — no secret needed.
const webhookPolicyNone = `
name: test-none-policy
trigger:
  type: webhook
  auth: none
agent:
  model: claude-opus-4-5
  task: "test task"
`

const testSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fakeSecretLoader is a test double that returns a fixed secret for a given policy.
type fakeSecretLoader struct {
	secrets map[string]string // policyID → plaintext secret
	err     error             // if non-nil, always returned
}

func (f *fakeSecretLoader) LoadWebhookSecret(_ context.Context, policyID string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	s, ok := f.secrets[policyID]
	if !ok {
		return "", trigger.ErrNoSecret
	}
	return s, nil
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
	r.Use(httputil.BodySizeLimit(httputil.MaxRequestBodySize))
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

// newHandler builds a WebhookHandler with the given secret loader.
func newHandler(t *testing.T, store *db.Store, loader trigger.SecretLoaderInterface) *trigger.WebhookHandler {
	t.Helper()
	registry := mcp.NewRegistry(store.Queries())
	noopClient := testutil.NewNoopLLMClient()
	providerReg := llm.NewProviderRegistry()
	providerReg.Register("anthropic", noopClient)
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, run.NewRunManager(), run.NewAgentFactory(providerReg), nil, 0, resolver)
	return trigger.NewWebhookHandler(store, launcher, loader, resolver)
}

func TestWebhookHandler(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T, store *db.Store)
		loader     func(store *db.Store) trigger.SecretLoaderInterface
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
		// auth: none
		{
			name: "auth none: 202 without any credential",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-none", webhookPolicyNone)
			},
			policyID:   "p-none",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		// auth: hmac
		{
			name: "hmac: 401 missing signature",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-hmac-missing", webhookPolicyHMAC)
			},
			loader: func(store *db.Store) trigger.SecretLoaderInterface {
				return &fakeSecretLoader{secrets: map[string]string{"p-hmac-missing": testSecret}}
			},
			policyID:   "p-hmac-missing",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "hmac: 403 invalid signature",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-hmac-bad", webhookPolicyHMAC)
			},
			loader: func(store *db.Store) trigger.SecretLoaderInterface {
				return &fakeSecretLoader{secrets: map[string]string{"p-hmac-bad": testSecret}}
			},
			policyID: "p-hmac-bad",
			body:     `{"event": "test"}`,
			headers: map[string]string{
				"X-Gleipnir-Signature": "sha256=deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "hmac: 202 valid signature",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-hmac-ok", webhookPolicyHMAC)
			},
			loader: func(store *db.Store) trigger.SecretLoaderInterface {
				return &fakeSecretLoader{secrets: map[string]string{"p-hmac-ok": testSecret}}
			},
			policyID: "p-hmac-ok",
			body:     `{"event": "test"}`,
			headers: map[string]string{
				"X-Gleipnir-Signature": computeTestSignature(testSecret, `{"event": "test"}`),
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name: "hmac: 500 no secret stored",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-hmac-nosecret", webhookPolicyHMAC)
			},
			loader: func(store *db.Store) trigger.SecretLoaderInterface {
				return &fakeSecretLoader{secrets: map[string]string{}} // empty — ErrNoSecret
			},
			policyID:   "p-hmac-nosecret",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "hmac: 500 encryption key missing",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-hmac-nokey", webhookPolicyHMAC)
			},
			loader: func(store *db.Store) trigger.SecretLoaderInterface {
				return &fakeSecretLoader{err: trigger.ErrEncryptionKeyMissing}
			},
			policyID:   "p-hmac-nokey",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusInternalServerError,
		},
		// auth: bearer
		{
			name: "bearer: 202 valid token",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-bearer-ok", webhookPolicyBearer)
			},
			loader: func(store *db.Store) trigger.SecretLoaderInterface {
				return &fakeSecretLoader{secrets: map[string]string{"p-bearer-ok": testSecret}}
			},
			policyID:   "p-bearer-ok",
			body:       `{"event": "test"}`,
			headers:    map[string]string{"Authorization": "Bearer " + testSecret},
			wantStatus: http.StatusAccepted,
		},
		{
			name: "bearer: 401 missing header",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-bearer-missing", webhookPolicyBearer)
			},
			loader: func(store *db.Store) trigger.SecretLoaderInterface {
				return &fakeSecretLoader{secrets: map[string]string{"p-bearer-missing": testSecret}}
			},
			policyID:   "p-bearer-missing",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "bearer: 401 malformed prefix",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-bearer-malformed", webhookPolicyBearer)
			},
			loader: func(store *db.Store) trigger.SecretLoaderInterface {
				return &fakeSecretLoader{secrets: map[string]string{"p-bearer-malformed": testSecret}}
			},
			policyID:   "p-bearer-malformed",
			body:       `{"event": "test"}`,
			headers:    map[string]string{"Authorization": "Token " + testSecret},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "bearer: 403 wrong token",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-bearer-wrong", webhookPolicyBearer)
			},
			loader: func(store *db.Store) trigger.SecretLoaderInterface {
				return &fakeSecretLoader{secrets: map[string]string{"p-bearer-wrong": testSecret}}
			},
			policyID:   "p-bearer-wrong",
			body:       `{"event": "test"}`,
			headers:    map[string]string{"Authorization": "Bearer wrongtoken"},
			wantStatus: http.StatusForbidden,
		},
		// auth: none — explicit opt-out, no credential check
		{
			name: "202 no secret configured (open webhook)",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-no-secret", minimalWebhookPolicy)
			},
			policyID:   "p-no-secret",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name: "409 when policy is paused",
			setup: func(t *testing.T, store *db.Store) {
				insertTestPolicy(t, store, "p-paused", minimalWebhookPolicy)
				now := time.Now().UTC().Format(time.RFC3339Nano)
				if err := store.SetPolicyPausedAt(context.Background(), db.SetPolicyPausedAtParams{
					PausedAt: &now,
					ID:       "p-paused",
				}); err != nil {
					t.Fatalf("SetPolicyPausedAt: %v", err)
				}
			},
			policyID:   "p-paused",
			body:       `{"event": "test"}`,
			wantStatus: http.StatusConflict,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			var loader trigger.SecretLoaderInterface
			if tc.loader != nil {
				loader = tc.loader(store)
			} else {
				loader = trigger.NewSecretLoader(store.Queries(), nil)
			}

			h := newHandler(t, store, loader)
			w := callHandlerWithHeaders(t, h, tc.policyID, tc.body, tc.headers)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

// TestWebhookHandler_RotateInvalidation verifies that after a secret is rotated,
// a request signed with the old secret is rejected.
func TestWebhookHandler_RotateInvalidation(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestPolicy(t, store, "p-rotate", webhookPolicyHMAC)
	body := `{"event": "test"}`

	secretA := testSecret
	secretB := strings.Repeat("b", 64)

	loaderA := &fakeSecretLoader{secrets: map[string]string{"p-rotate": secretA}}
	loaderB := &fakeSecretLoader{secrets: map[string]string{"p-rotate": secretB}}

	// Sign with secret A; loader has A → 202.
	hA := newHandler(t, store, loaderA)
	wA := callHandlerWithHeaders(t, hA, "p-rotate", body, map[string]string{
		"X-Gleipnir-Signature": computeTestSignature(secretA, body),
	})
	if wA.Code != http.StatusAccepted {
		t.Fatalf("before rotate: got %d, want 202", wA.Code)
	}

	// After rotate (loader now returns B), resend original request signed with A → 403.
	hB := newHandler(t, store, loaderB)
	wB := callHandlerWithHeaders(t, hB, "p-rotate", body, map[string]string{
		"X-Gleipnir-Signature": computeTestSignature(secretA, body),
	})
	if wB.Code != http.StatusForbidden {
		t.Errorf("after rotate: got %d, want 403", wB.Code)
	}
}

// TestWebhookHandler_HMACMissingSignatureHeader checks the exact error code returned
// when the signature header is absent vs. present but wrong.
func TestWebhookHandler_HMACSignatureErrors(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestPolicy(t, store, "p-sig-errs", webhookPolicyHMAC)
	loader := &fakeSecretLoader{secrets: map[string]string{"p-sig-errs": testSecret}}
	h := newHandler(t, store, loader)

	t.Run("missing header → 401", func(t *testing.T) {
		w := callHandler(t, h, "p-sig-errs", `{"x":1}`)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("got %d, want 401", w.Code)
		}
	})

	t.Run("malformed prefix → 403", func(t *testing.T) {
		// The handler maps any non-missing verify error to 403.
		w := callHandlerWithHeaders(t, h, "p-sig-errs", `{"x":1}`, map[string]string{
			"X-Gleipnir-Signature": "md5=abc123",
		})
		if w.Code != http.StatusForbidden {
			t.Errorf("got %d, want 403", w.Code)
		}
	})
}

func TestWebhookHandler_RunCreatedInDB(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestPolicy(t, store, "p-run-created", minimalWebhookPolicy)

	registry := mcp.NewRegistry(store.Queries())
	noopClient := testutil.NewNoopLLMClient()
	providerReg := llm.NewProviderRegistry()
	providerReg.Register("anthropic", noopClient)
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, run.NewRunManager(), run.NewAgentFactory(providerReg), nil, 0, resolver)
	h := trigger.NewWebhookHandler(store, launcher, trigger.NewSecretLoader(store.Queries(), nil), resolver)

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

// TestErrSentinels verifies that the sentinel errors are exported correctly.
func TestErrSentinels(t *testing.T) {
	if !errors.Is(trigger.ErrNoSecret, trigger.ErrNoSecret) {
		t.Error("ErrNoSecret is not itself")
	}
	if !errors.Is(trigger.ErrEncryptionKeyMissing, trigger.ErrEncryptionKeyMissing) {
		t.Error("ErrEncryptionKeyMissing is not itself")
	}
}

// webhookPolicyNoModel is a policy with no model block — used to test the
// "no default model configured" 500 response when the system default is unset.
const webhookPolicyNoModel = `
name: test-no-model-policy
trigger:
  type: webhook
  auth: none
agent:
  task: "test task"
`

// TestWebhookHandler_Returns500_WhenNoDefaultModelAndPolicyOmitsModel verifies
// that when the system default is unset (sql.ErrNoRows from the resolver) and
// the policy YAML omits the model block, the webhook handler returns 500 with
// a message containing "no default model configured".
func TestWebhookHandler_Returns500_WhenNoDefaultModelAndPolicyOmitsModel(t *testing.T) {
	store := testutil.NewTestStore(t)
	insertTestPolicy(t, store, "p-no-model-500", webhookPolicyNoModel)

	registry := mcp.NewRegistry(store.Queries())
	noopClient := testutil.NewNoopLLMClient()
	providerReg := llm.NewProviderRegistry()
	providerReg.Register("anthropic", noopClient)

	// Resolver with no default configured.
	noDefault := stubDefaultModelResolver{err: sql.ErrNoRows}
	launcher := run.NewRunLauncher(store, registry, run.NewRunManager(), run.NewAgentFactory(providerReg), nil, 0, noDefault)
	h := trigger.NewWebhookHandler(store, launcher, trigger.NewSecretLoader(store.Queries(), nil), noDefault)

	w := callHandler(t, h, "p-no-model-500", `{"event": "test"}`)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no default model configured") {
		t.Errorf("expected body to contain %q, got: %s", "no default model configured", w.Body.String())
	}
}

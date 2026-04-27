package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// fakeWebhookEncrypter implements policy.SecretCipher for use in handler tests.
type fakeWebhookEncrypter struct{}

func (f *fakeWebhookEncrypter) EncryptWebhookSecret(plaintext string) (string, error) {
	return "ENC[" + plaintext + "]", nil
}

func (f *fakeWebhookEncrypter) DecryptWebhookSecret(ciphertext string) (string, error) {
	var inner string
	if _, err := fmt.Sscanf(ciphertext, "ENC[%s", &inner); err != nil {
		return "", fmt.Errorf("bad fake ciphertext %q", ciphertext)
	}
	// Sscanf stops at whitespace, so strip the trailing ']' we appended.
	if len(inner) > 0 && inner[len(inner)-1] == ']' {
		inner = inner[:len(inner)-1]
	}
	return inner, nil
}

// newWebhookSecretRouter wires the PolicyWebhookHandler under the same URL
// pattern used in production: POST /policies/{id}/webhook/rotate and
// GET /policies/{id}/webhook/secret.
func newWebhookSecretRouter(t *testing.T) (http.Handler, *policy.Service) {
	t.Helper()
	store := testutil.NewTestStore(t)
	svc := policy.NewService(store, nil, nil, nil, nil)
	svc.WithWebhookSecretEncrypter(&fakeWebhookEncrypter{})

	h := api.NewPolicyWebhookHandler(svc)
	r := chi.NewRouter()
	r.Post("/policies/{id}/webhook/rotate", h.Rotate)
	r.Get("/policies/{id}/webhook/secret", h.Get)
	return r, svc
}

const webhookPolicyYAML = `
name: test-webhook
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
agent:
  task: run
`

const manualPolicyYAML = `
name: test-manual
trigger:
  type: manual
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
agent:
  task: run
`

func createHandlerPolicy(t *testing.T, svc *policy.Service, yaml string) string {
	t.Helper()
	result, err := svc.Create(context.Background(), yaml)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return result.Policy.ID
}

// TestPolicyWebhookRotate_ReturnsSecret verifies the Rotate endpoint returns a
// 64-char hex secret in the data envelope.
func TestPolicyWebhookRotate_ReturnsSecret(t *testing.T) {
	r, svc := newWebhookSecretRouter(t)
	id := createHandlerPolicy(t, svc, webhookPolicyYAML)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/policies/"+id+"/webhook/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var envelope struct {
		Data struct {
			Secret string `json:"secret"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data.Secret) != 64 {
		t.Errorf("secret length = %d, want 64", len(envelope.Data.Secret))
	}
}

// TestPolicyWebhookGet_ReturnsSecret verifies the Get endpoint decrypts and
// returns the secret after a rotate.
func TestPolicyWebhookGet_ReturnsSecret(t *testing.T) {
	r, svc := newWebhookSecretRouter(t)
	id := createHandlerPolicy(t, svc, webhookPolicyYAML)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	// Rotate first to populate the column.
	rotateResp, err := http.Post(srv.URL+"/policies/"+id+"/webhook/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer rotateResp.Body.Close()
	var rotateEnvelope struct {
		Data struct {
			Secret string `json:"secret"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rotateResp.Body).Decode(&rotateEnvelope); err != nil {
		t.Fatalf("decode rotate: %v", err)
	}

	// Get the secret back.
	getResp, err := http.Get(srv.URL + "/policies/" + id + "/webhook/secret")
	if err != nil {
		t.Fatalf("GET secret: %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", getResp.StatusCode)
	}

	var getEnvelope struct {
		Data struct {
			Secret string `json:"secret"`
		} `json:"data"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&getEnvelope); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if getEnvelope.Data.Secret != rotateEnvelope.Data.Secret {
		t.Errorf("Get returned %q, want %q", getEnvelope.Data.Secret, rotateEnvelope.Data.Secret)
	}
}

// TestPolicyWebhookGet_NoSecret verifies that Get returns 404 with error code
// "no_secret" when the policy exists but no secret has been rotated yet.
func TestPolicyWebhookGet_NoSecret(t *testing.T) {
	r, svc := newWebhookSecretRouter(t)
	id := createHandlerPolicy(t, svc, webhookPolicyYAML)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/policies/" + id + "/webhook/secret")
	if err != nil {
		t.Fatalf("GET secret: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}

	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error != "no_secret" {
		t.Errorf("error = %q, want %q", envelope.Error, "no_secret")
	}
}

// TestPolicyWebhookRotate_NotWebhookPolicy verifies that rotating on a
// non-webhook policy returns 409.
func TestPolicyWebhookRotate_NotWebhookPolicy(t *testing.T) {
	r, svc := newWebhookSecretRouter(t)
	id := createHandlerPolicy(t, svc, manualPolicyYAML)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/policies/"+id+"/webhook/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// TestPolicyWebhookRotate_PolicyNotFound verifies that rotating for a missing
// policy returns 404.
func TestPolicyWebhookRotate_PolicyNotFound(t *testing.T) {
	r, _ := newWebhookSecretRouter(t)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/policies/nonexistent-id/webhook/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestPolicyWebhookRotate_EncryptionUnavailable verifies that a missing
// encryption key returns 503.
func TestPolicyWebhookRotate_EncryptionUnavailable(t *testing.T) {
	store := testutil.NewTestStore(t)
	// No encrypter set — simulates GLEIPNIR_ENCRYPTION_KEY absent.
	svc := policy.NewService(store, nil, nil, nil, nil)

	h := api.NewPolicyWebhookHandler(svc)
	r := chi.NewRouter()
	r.Post("/policies/{id}/webhook/rotate", h.Rotate)
	r.Get("/policies/{id}/webhook/secret", h.Get)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	id := createHandlerPolicy(t, svc, webhookPolicyYAML)

	resp, err := http.Post(srv.URL+"/policies/"+id+"/webhook/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

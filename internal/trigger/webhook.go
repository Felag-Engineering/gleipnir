// Package trigger implements run trigger handlers (webhook, manual, scheduled).
package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/execution/run"
	"github.com/rapp992/gleipnir/internal/http/httputil"
	"github.com/rapp992/gleipnir/internal/model"
)

// SecretLoaderInterface is the interface the webhook handler uses to load a
// policy's decrypted secret on demand. Exported so tests can inject fakes.
type SecretLoaderInterface interface {
	LoadWebhookSecret(ctx context.Context, policyID string) (secret string, err error)
}

// WebhookHandler handles POST /api/v1/webhooks/{policyID}.
// It validates the policy exists, applies the concurrency policy, creates a
// run record, and launches the agent in a goroutine.
type WebhookHandler struct {
	store         *db.Store
	launcher      *run.RunLauncher
	secretLoader  SecretLoaderInterface
	modelResolver defaultModelResolver
}

// NewWebhookHandler returns a WebhookHandler backed by store, launcher, the
// provided secret loader, and the given resolver for the system default model.
// secretLoader is required; pass NewSecretLoader(q, key).
func NewWebhookHandler(store *db.Store, launcher *run.RunLauncher, secretLoader SecretLoaderInterface, modelResolver defaultModelResolver) *WebhookHandler {
	return &WebhookHandler{
		store:         store,
		launcher:      launcher,
		secretLoader:  secretLoader,
		modelResolver: modelResolver,
	}
}

// Handle is the chi-compatible HTTP handler for webhook-triggered runs.
// Responds 202 Accepted with {"data": {"run_id": "..."}} on success.
// Responds 202 Accepted with {"data": {"queued": true}} when enqueued (concurrency: queue).
// Responds 400 if the request body is not valid JSON.
// Responds 401 if the required credential is absent.
// Responds 403 if the credential is present but invalid.
// Responds 404 if the policy does not exist.
// Responds 409 if the policy is paused or the concurrency policy is skip and a run is already active.
// Responds 429 if the trigger queue is full (concurrency: queue).
// Responds 500 if auth mode is hmac/bearer but the secret cannot be loaded.
func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "policyID")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "failed to read body", "")
		return
	}

	if !json.Valid(body) {
		httputil.WriteError(w, http.StatusBadRequest, "request body must be valid JSON", "")
		return
	}

	ctx := r.Context()

	parsed := fetchAndParsePolicy(ctx, w, h.store, policyID, h.modelResolver)
	if parsed == nil {
		return
	}

	if !h.authenticate(w, r, ctx, policyID, body, parsed.Trigger.WebhookAuth) {
		return
	}

	checkConcurrencyAndLaunch(ctx, w, h.launcher, run.LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: string(body),
		ParsedPolicy:   parsed,
	}, parsed.Agent.Concurrency, parsed.Agent.QueueDepth, "webhook")
}

// authenticate verifies the incoming request's credentials based on the
// policy's configured auth mode. Returns true when the request should proceed,
// false when it has already been rejected with an appropriate error response.
func (h *WebhookHandler) authenticate(
	w http.ResponseWriter, r *http.Request, //nolint:revive
	ctx context.Context, policyID string, body []byte,
	authMode model.WebhookAuthMode,
) bool {
	switch authMode {
	case model.WebhookAuthNone, "":
		return true

	case model.WebhookAuthHMAC:
		secret, err := h.secretLoader.LoadWebhookSecret(ctx, policyID)
		if err != nil {
			return h.handleSecretLoadError(w, err)
		}
		sigHeader := r.Header.Get(SignatureHeader)
		if verifyErr := ValidateSignature(secret, body, sigHeader); verifyErr != nil {
			if errors.Is(verifyErr, errMissingSignature) {
				httputil.WriteError(w, http.StatusUnauthorized, "missing signature", "")
			} else {
				httputil.WriteError(w, http.StatusForbidden, "invalid signature", "")
			}
			return false
		}
		return true

	case model.WebhookAuthBearer:
		secret, err := h.secretLoader.LoadWebhookSecret(ctx, policyID)
		if err != nil {
			return h.handleSecretLoadError(w, err)
		}
		if verifyErr := ValidateBearer(secret, r.Header.Get("Authorization")); verifyErr != nil {
			if errors.Is(verifyErr, errMissingBearer) {
				httputil.WriteError(w, http.StatusUnauthorized, "missing or malformed Authorization header", "")
			} else {
				httputil.WriteError(w, http.StatusForbidden, "invalid bearer token", "")
			}
			return false
		}
		return true

	default:
		// Unknown auth mode — treat as a configuration error.
		httputil.WriteError(w, http.StatusInternalServerError, "unknown webhook auth mode", "")
		return false
	}
}

// handleSecretLoadError maps secret loader errors to HTTP responses.
// Returns false in all cases (the request should not proceed).
func (h *WebhookHandler) handleSecretLoadError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, ErrNoSecret):
		httputil.WriteError(w, http.StatusInternalServerError,
			"webhook misconfigured: hmac mode but no secret stored; rotate a secret first", "")
	case errors.Is(err, ErrEncryptionKeyMissing):
		slog.Error("webhook request rejected: encryption key not configured but policy has encrypted secret")
		httputil.WriteError(w, http.StatusInternalServerError,
			"webhook misconfigured: encryption key not set", "")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, "failed to load webhook secret", "")
	}
	return false
}

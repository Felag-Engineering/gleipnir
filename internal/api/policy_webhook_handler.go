package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/httputil"
	"github.com/rapp992/gleipnir/internal/policy"
)

// PolicyWebhookHandler serves the rotate and reveal endpoints for webhook secrets.
//
// The plaintext secret is written only to the response body via WriteJSON. It
// MUST NOT be passed to slog, returned in any other handler, or stored on any
// in-memory field beyond the call stack. Tests assert this.
type PolicyWebhookHandler struct {
	svc *policy.Service
}

// NewPolicyWebhookHandler returns a PolicyWebhookHandler backed by svc.
func NewPolicyWebhookHandler(svc *policy.Service) *PolicyWebhookHandler {
	return &PolicyWebhookHandler{svc: svc}
}

// Rotate generates and stores a fresh webhook secret for the policy, returning
// the plaintext in {"data": {"secret": "<64 hex>"}}. This is the only endpoint
// that returns the plaintext secret — it is never logged.
//
// POST /api/v1/policies/{id}/webhook/rotate
func (h *PolicyWebhookHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	plaintext, err := h.svc.RotateWebhookSecret(ctx, id)
	if err != nil {
		h.writeSecretError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"secret": plaintext})
}

// Get reveals the stored webhook secret for the policy. Returns 404 with
// error code "no_secret" when no secret has been rotated yet, so the frontend
// can distinguish "never configured" from a transport error.
//
// GET /api/v1/policies/{id}/webhook/secret
func (h *PolicyWebhookHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	plaintext, err := h.svc.GetWebhookSecret(ctx, id)
	if err != nil {
		if errors.Is(err, policy.ErrNoSecret) {
			httputil.WriteError(w, http.StatusNotFound, "no_secret", "no webhook secret has been set for this policy; call rotate to generate one")
			return
		}
		h.writeSecretError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"secret": plaintext})
}

// writeSecretError maps service-level errors to HTTP responses.
func (h *PolicyWebhookHandler) writeSecretError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, policy.ErrNoSuchPolicy):
		httputil.WriteError(w, http.StatusNotFound, "policy not found", "")
	case errors.Is(err, policy.ErrNotWebhookTrigger):
		httputil.WriteError(w, http.StatusConflict, "policy is not a webhook trigger", "only webhook-type policies have a shared secret")
	case errors.Is(err, policy.ErrEncryptionUnavailable):
		httputil.WriteError(w, http.StatusServiceUnavailable, "encryption key not configured", "set GLEIPNIR_ENCRYPTION_KEY to enable webhook secret management")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
	}
}

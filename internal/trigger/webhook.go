// Package trigger implements run trigger handlers (webhook, manual, scheduled).
package trigger

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/httputil"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/run"
)

// WebhookHandler handles POST /api/v1/webhooks/{policyID}.
// It validates the policy exists, applies the concurrency policy, creates a
// run record, and launches the agent in a goroutine.
type WebhookHandler struct {
	store    *db.Store
	launcher *run.RunLauncher
}

// NewWebhookHandler returns a WebhookHandler backed by store and launcher.
func NewWebhookHandler(store *db.Store, launcher *run.RunLauncher) *WebhookHandler {
	return &WebhookHandler{
		store:    store,
		launcher: launcher,
	}
}

// Handle is the chi-compatible HTTP handler for webhook-triggered runs.
// Responds 202 Accepted with {"data": {"run_id": "..."}} on success.
// Responds 202 Accepted with {"data": {"queued": true}} when enqueued (concurrency: queue).
// Responds 400 if the request body is not valid JSON.
// Responds 404 if the policy does not exist.
// Responds 409 if the policy is paused or the concurrency policy is skip and a run is already active.
// Responds 429 if the trigger queue is full (concurrency: queue).
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

	parsed := fetchAndParsePolicy(ctx, w, h.store, policyID)
	if parsed == nil {
		return
	}

	if parsed.Trigger.WebhookSecret != "" {
		sigHeader := r.Header.Get(SignatureHeader)
		if err := ValidateSignature(parsed.Trigger.WebhookSecret, body, sigHeader); err != nil {
			if errors.Is(err, errMissingSignature) {
				httputil.WriteError(w, http.StatusUnauthorized, "missing signature", "")
			} else {
				httputil.WriteError(w, http.StatusForbidden, "invalid signature", "")
			}
			return
		}
	}

	checkConcurrencyAndLaunch(ctx, w, h.launcher, run.LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: string(body),
		ParsedPolicy:   parsed,
	}, parsed.Agent.Concurrency, parsed.Agent.QueueDepth, "webhook")
}

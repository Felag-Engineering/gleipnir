// Package trigger implements run trigger handlers (webhook, manual, scheduled).
package trigger

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// WebhookHandler handles POST /api/v1/webhooks/{policyID}.
// It validates the policy exists, applies the concurrency policy, creates a
// run record, and launches the agent in a goroutine.
type WebhookHandler struct {
	store    *db.Store
	launcher *RunLauncher
}

// NewWebhookHandler returns a WebhookHandler backed by store and launcher.
func NewWebhookHandler(store *db.Store, launcher *RunLauncher) *WebhookHandler {
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
// Responds 409 if the concurrency policy is skip and a run is already active.
// Responds 429 if the trigger queue is full (concurrency: queue).
func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "policyID")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "failed to read body", "")
		return
	}

	if !json.Valid(body) {
		api.WriteError(w, http.StatusBadRequest, "request body must be valid JSON", "")
		return
	}

	ctx := r.Context()

	dbPolicy, err := h.store.GetPolicy(ctx, policyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			api.WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		api.WriteError(w, http.StatusInternalServerError, "failed to load policy", "")
		return
	}

	parsed, err := policy.Parse(dbPolicy.Yaml, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to parse policy", "")
		return
	}

	if parsed.Trigger.WebhookSecret != "" {
		sigHeader := r.Header.Get(SignatureHeader)
		if err := ValidateSignature(parsed.Trigger.WebhookSecret, body, sigHeader); err != nil {
			if errors.Is(err, errMissingSignature) {
				api.WriteError(w, http.StatusUnauthorized, "missing signature", "")
			} else {
				api.WriteError(w, http.StatusForbidden, "invalid signature", "")
			}
			return
		}
	}

	if err := h.launcher.CheckConcurrency(ctx, policyID, parsed.Agent.Concurrency); err != nil {
		switch {
		case errors.Is(err, ErrConcurrencySkipActive):
			api.WriteError(w, http.StatusConflict, "run already active for this policy (concurrency: skip)", "")
		case errors.Is(err, ErrConcurrencyQueueActive):
			if enqErr := h.launcher.Enqueue(ctx, LaunchParams{
				PolicyID:       policyID,
				TriggerType:    model.TriggerTypeWebhook,
				TriggerPayload: string(body),
				ParsedPolicy:   parsed,
			}, parsed.Agent.QueueDepth); enqErr != nil {
				if errors.Is(enqErr, ErrConcurrencyQueueFull) {
					api.WriteError(w, http.StatusTooManyRequests, "trigger queue is full", "")
				} else {
					slog.ErrorContext(ctx, "webhook: failed to enqueue trigger", "policy_id", policyID, "err", enqErr)
					api.WriteError(w, http.StatusInternalServerError, "failed to enqueue trigger", "")
				}
				return
			}
			api.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true})
			return
		case errors.Is(err, ErrConcurrencyUnrecognised):
			api.WriteError(w, http.StatusInternalServerError, "unrecognised concurrency policy", "")
		default:
			slog.ErrorContext(ctx, "webhook: failed to check active runs", "policy_id", policyID, "err", err)
			api.WriteError(w, http.StatusInternalServerError, "failed to check active runs", "")
		}
		return
	}

	result, err := h.launcher.Launch(ctx, LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: string(body),
		ParsedPolicy:   parsed,
	})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to launch run", "")
		return
	}

	api.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": result.RunID})
}

package trigger

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/httputil"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/run"
)

// ManualTriggerHandler handles POST /api/v1/policies/{policyID}/trigger.
// It validates the policy exists, applies the concurrency policy, creates a
// run record with trigger_type: manual, and launches the agent in a goroutine.
type ManualTriggerHandler struct {
	store    *db.Store
	launcher *run.RunLauncher
}

// NewManualTriggerHandler returns a ManualTriggerHandler backed by store and launcher.
func NewManualTriggerHandler(store *db.Store, launcher *run.RunLauncher) *ManualTriggerHandler {
	return &ManualTriggerHandler{
		store:    store,
		launcher: launcher,
	}
}

// Handle is the chi-compatible HTTP handler for manually-triggered runs.
// Responds 202 Accepted with {"data": {"run_id": "..."}} on success.
// Responds 202 Accepted with {"data": {"queued": true}} when enqueued (concurrency: queue).
// The optional request body is passed as the trigger payload. An empty body
// is treated as '{}'.
// Responds 400 if the request body is not valid JSON.
// Responds 404 if the policy does not exist.
// Responds 409 if the concurrency policy is skip and a run is already active.
// Responds 429 if the trigger queue is full (concurrency: queue).
func (h *ManualTriggerHandler) Handle(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "policyID")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "failed to read body", "")
		return
	}

	// Treat an empty body as an empty JSON object.
	if len(body) == 0 {
		body = []byte("{}")
	}

	if !json.Valid(body) {
		httputil.WriteError(w, http.StatusBadRequest, "request body must be valid JSON", "")
		return
	}

	ctx := r.Context()

	dbPolicy, err := h.store.GetPolicy(ctx, policyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to load policy", "")
		return
	}

	parsed, err := policy.Parse(dbPolicy.Yaml, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to parse policy", "")
		return
	}

	if err := h.launcher.CheckConcurrency(ctx, policyID, parsed.Agent.Concurrency); err != nil {
		switch {
		case errors.Is(err, run.ErrConcurrencySkipActive):
			httputil.WriteError(w, http.StatusConflict, "run already active for this policy (concurrency: skip)", "")
		case errors.Is(err, run.ErrConcurrencyQueueActive):
			if enqErr := h.launcher.Enqueue(ctx, run.LaunchParams{
				PolicyID:       policyID,
				TriggerType:    model.TriggerTypeManual,
				TriggerPayload: string(body),
				ParsedPolicy:   parsed,
			}, parsed.Agent.QueueDepth); enqErr != nil {
				if errors.Is(enqErr, run.ErrConcurrencyQueueFull) {
					httputil.WriteError(w, http.StatusTooManyRequests, "trigger queue is full", "")
				} else {
					slog.ErrorContext(ctx, "manual trigger: failed to enqueue trigger", "policy_id", policyID, "err", enqErr)
					httputil.WriteError(w, http.StatusInternalServerError, "failed to enqueue trigger", "")
				}
				return
			}
			httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true})
			return
		case errors.Is(err, run.ErrConcurrencyUnrecognised):
			httputil.WriteError(w, http.StatusInternalServerError, "unrecognised concurrency policy", "")
		default:
			slog.ErrorContext(ctx, "manual trigger: failed to check active runs", "policy_id", policyID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "failed to check active runs", "")
		}
		return
	}

	result, err := h.launcher.Launch(ctx, run.LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypeManual,
		TriggerPayload: string(body),
		ParsedPolicy:   parsed,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to launch run", "")
		return
	}

	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": result.RunID})
}

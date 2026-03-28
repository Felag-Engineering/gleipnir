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

// ManualTriggerHandler handles POST /api/v1/policies/{policyID}/trigger.
// It validates the policy exists, applies the concurrency policy, creates a
// run record with trigger_type: manual, and launches the agent in a goroutine.
type ManualTriggerHandler struct {
	store    *db.Store
	launcher *RunLauncher
}

// NewManualTriggerHandler returns a ManualTriggerHandler backed by store and launcher.
func NewManualTriggerHandler(store *db.Store, launcher *RunLauncher) *ManualTriggerHandler {
	return &ManualTriggerHandler{
		store:    store,
		launcher: launcher,
	}
}

// Handle is the chi-compatible HTTP handler for manually-triggered runs.
// Responds 202 Accepted with {"data": {"run_id": "..."}} on success.
// The optional request body is passed as the trigger payload. An empty body
// is treated as '{}'.
// Responds 400 if the request body is not valid JSON.
// Responds 404 if the policy does not exist.
// Responds 409 if the concurrency policy is skip and a run is already active.
// Responds 501 if the concurrency policy is queue or replace (not yet implemented).
func (h *ManualTriggerHandler) Handle(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "policyID")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "failed to read body", "")
		return
	}

	// Treat an empty body as an empty JSON object.
	if len(body) == 0 {
		body = []byte("{}")
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

	parsed, err := policy.Parse(dbPolicy.Yaml)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to parse policy", "")
		return
	}

	if err := h.launcher.CheckConcurrency(ctx, policyID, parsed.Agent.Concurrency); err != nil {
		switch {
		case errors.Is(err, ErrConcurrencySkipActive):
			api.WriteError(w, http.StatusConflict, "run already active for this policy (concurrency: skip)", "")
		case errors.Is(err, ErrConcurrencyNotImplemented):
			api.WriteError(w, http.StatusNotImplemented, "concurrency policy not implemented", "")
		case errors.Is(err, ErrConcurrencyUnrecognised):
			api.WriteError(w, http.StatusInternalServerError, "unrecognised concurrency policy", "")
		default:
			slog.ErrorContext(ctx, "manual trigger: failed to check active runs", "policy_id", policyID, "err", err)
			api.WriteError(w, http.StatusInternalServerError, "failed to check active runs", "")
		}
		return
	}

	result, err := h.launcher.Launch(ctx, LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypeManual,
		TriggerPayload: string(body),
		ParsedPolicy:   parsed,
	})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to launch run", "")
		return
	}

	api.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": result.RunID})
}

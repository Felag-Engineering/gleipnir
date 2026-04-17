package trigger

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/httputil"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/run"
)

// ManualTriggerHandler handles POST /api/v1/policies/{policyID}/trigger.
// It validates the policy exists, applies the concurrency policy, creates a
// run record with trigger_type: manual, and launches the agent in a goroutine.
type ManualTriggerHandler struct {
	store         *db.Store
	launcher      *run.RunLauncher
	modelResolver defaultModelResolver
}

// NewManualTriggerHandler returns a ManualTriggerHandler backed by store, launcher,
// and the given resolver for fetching the system default model.
func NewManualTriggerHandler(store *db.Store, launcher *run.RunLauncher, modelResolver defaultModelResolver) *ManualTriggerHandler {
	return &ManualTriggerHandler{
		store:         store,
		launcher:      launcher,
		modelResolver: modelResolver,
	}
}

// Handle is the chi-compatible HTTP handler for manually-triggered runs.
// Responds 202 Accepted with {"data": {"run_id": "..."}} on success.
// Responds 202 Accepted with {"data": {"queued": true}} when enqueued (concurrency: queue).
// The optional request body is passed as the trigger payload. An empty body
// is treated as '{}'.
// Responds 400 if the request body is not valid JSON.
// Responds 404 if the policy does not exist.
// Responds 409 if the policy is paused or the concurrency policy is skip and a run is already active.
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

	parsed := fetchAndParsePolicy(ctx, w, h.store, policyID, h.modelResolver)
	if parsed == nil {
		return
	}

	checkConcurrencyAndLaunch(ctx, w, h.launcher, run.LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypeManual,
		TriggerPayload: string(body),
		ParsedPolicy:   parsed,
	}, parsed.Agent.Concurrency, parsed.Agent.QueueDepth, "manual trigger")
}

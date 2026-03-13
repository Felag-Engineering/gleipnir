package trigger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// ManualTriggerHandler handles POST /api/v1/policies/{policyID}/trigger.
// It validates the policy exists, applies the concurrency policy, creates a
// run record with trigger_type: manual, and launches the agent in a goroutine.
type ManualTriggerHandler struct {
	store     *db.Store
	registry  *mcp.Registry
	manager   *RunManager
	newAgent  AgentFactory
	publisher agent.Publisher
}

// NewManualTriggerHandler returns a ManualTriggerHandler backed by store, registry, manager, factory, and publisher.
// publisher may be nil, in which case no real-time events are emitted.
func NewManualTriggerHandler(store *db.Store, registry *mcp.Registry, manager *RunManager, factory AgentFactory, publisher agent.Publisher) *ManualTriggerHandler {
	return &ManualTriggerHandler{
		store:     store,
		registry:  registry,
		manager:   manager,
		newAgent:  factory,
		publisher: publisher,
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

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB limit
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

	switch parsed.Agent.Concurrency {
	case model.ConcurrencySkip:
		active, err := h.store.ListActiveRunsByPolicy(ctx, policyID)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "failed to check active runs", "")
			return
		}
		if len(active) > 0 {
			api.WriteError(w, http.StatusConflict, "run already active for this policy (concurrency: skip)", "")
			return
		}
	case model.ConcurrencyParallel:
		// proceed without concurrency checks
	case model.ConcurrencyQueue, model.ConcurrencyReplace:
		api.WriteError(w, http.StatusNotImplemented, "concurrency policy not implemented", "")
		return
	default:
		api.WriteError(w, http.StatusInternalServerError, "unrecognised concurrency policy", "")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	run, err := h.store.CreateRun(ctx, db.CreateRunParams{
		ID:             model.NewULID(),
		PolicyID:       policyID,
		TriggerType:    string(model.TriggerTypeManual),
		TriggerPayload: string(body),
		StartedAt:      now,
		CreatedAt:      now,
	})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to create run", "")
		return
	}

	tools, err := h.registry.ResolveForPolicy(ctx, parsed)
	if err != nil {
		// Mark the run failed before returning — it was created but cannot proceed.
		markRunFailed(h.store, run.ID, err)
		api.WriteError(w, http.StatusInternalServerError, "failed to resolve tools", "")
		return
	}

	audit := agent.NewAuditWriter(h.store.Queries, agent.WithPublisher(h.publisher))
	sm := agent.NewRunStateMachine(run.ID, model.RunStatusPending, h.store.Queries, agent.WithStateMachinePublisher(h.publisher))

	ba, err := h.newAgent(agent.Config{
		Tools:        tools,
		Policy:       parsed,
		Audit:        audit,
		StateMachine: sm,
		// ApprovalCh is an unbuffered channel that is never sent to.
		// Runs requiring approval will block until ScanOrphanedRuns marks
		// them interrupted on the next restart.
		ApprovalCh: make(chan bool),
	})
	if err != nil {
		// Mark the run failed — it was created and tools resolved but agent
		// construction failed (e.g. schema narrowing error). Without this,
		// the run stays in 'pending' forever since ScanOrphanedRuns only
		// rescues 'running' and 'waiting_for_approval' states.
		markRunFailed(h.store, run.ID, err)
		audit.Close()
		api.WriteError(w, http.StatusInternalServerError, "failed to construct agent", "")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.manager.Register(run.ID, cancel)

	api.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": run.ID})

	go func() {
		defer cancel()
		defer h.manager.Deregister(run.ID)
		if err := ba.Run(ctx, run.ID, string(body)); err != nil {
			slog.Error("manual run failed", "run_id", run.ID, "err", err)
		}
	}()
}

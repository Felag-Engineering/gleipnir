package trigger

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// PaginatedRunsResponse is the JSON envelope returned by List.
type PaginatedRunsResponse struct {
	Runs  []RunSummary `json:"runs"`
	Total int64        `json:"total"`
}

// RunSummary is the JSON shape returned for a single run.
type RunSummary struct {
	ID             string  `json:"id"`
	PolicyID       string  `json:"policy_id"`
	PolicyName     string  `json:"policy_name"`
	Status         string  `json:"status"`
	TriggerType    string  `json:"trigger_type"`
	TriggerPayload string  `json:"trigger_payload"`
	StartedAt      string  `json:"started_at"`
	CompletedAt    *string `json:"completed_at"`
	TokenCost      int64   `json:"token_cost"`
	Error          *string `json:"error"`
	CreatedAt      string  `json:"created_at"`
	SystemPrompt   *string `json:"system_prompt"`
}

// StepSummary is the JSON shape returned for a single run step.
type StepSummary struct {
	ID         string `json:"id"`
	RunID      string `json:"run_id"`
	StepNumber int64  `json:"step_number"`
	Type       string `json:"type"`
	Content    string `json:"content"`
	TokenCost  int64  `json:"token_cost"`
	CreatedAt  string `json:"created_at"`
}

// RunsHandler serves run inspection and control endpoints.
type RunsHandler struct {
	store   *db.Store
	manager *RunManager
}

// NewRunsHandler returns a RunsHandler backed by store and manager.
func NewRunsHandler(store *db.Store, manager *RunManager) *RunsHandler {
	return &RunsHandler{store: store, manager: manager}
}

// List handles GET /api/v1/runs with optional filters and pagination.
// Query params: policy_id, status, since (RFC3339), until (RFC3339),
// sort (only "started"), order ("asc"|"desc"), limit, offset.
func (h *RunsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	var policyID interface{}
	if v := q.Get("policy_id"); v != "" {
		policyID = v
	}

	var status interface{}
	if v := q.Get("status"); v != "" {
		if !model.RunStatus(v).Valid() {
			api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid status %q: must be one of pending, running, complete, failed, waiting_for_approval, interrupted", v), "")
			return
		}
		status = v
	}

	var since interface{}
	if v := q.Get("since"); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid since %q: must be RFC3339", v), "")
			return
		}
		since = v
	}

	var until interface{}
	if v := q.Get("until"); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid until %q: must be RFC3339", v), "")
			return
		}
		until = v
	}

	sort := q.Get("sort")
	if sort == "" {
		sort = "started"
	}
	if sort != "started" {
		api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid sort %q: must be \"started\"", sort), "")
		return
	}

	order := q.Get("order")
	if order == "" {
		order = "desc"
	}
	if order != "asc" && order != "desc" {
		api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid order %q: must be \"asc\" or \"desc\"", order), "")
		return
	}

	limit := int64(50)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	offset := int64(0)
	if v := q.Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n >= 0 {
			offset = n
		}
	}

	var rows []db.Run
	var err error
	if order == "asc" {
		rows, err = h.store.ListRunsAsc(ctx, db.ListRunsAscParams{
			PolicyID: policyID,
			Status:   status,
			Since:    since,
			Until:    until,
			Limit:    limit,
			Offset:   offset,
		})
	} else {
		rows, err = h.store.ListRuns(ctx, db.ListRunsParams{
			PolicyID: policyID,
			Status:   status,
			Since:    since,
			Until:    until,
			Limit:    limit,
			Offset:   offset,
		})
	}
	if err != nil {
		slog.Error("ListRuns query failed", "err", err)
		api.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	total, err := h.store.CountRuns(ctx, db.CountRunsParams{
		PolicyID: policyID,
		Status:   status,
		Since:    since,
		Until:    until,
	})
	if err != nil {
		slog.Error("CountRuns query failed", "err", err)
		api.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	// Fetch policy names for all unique policy IDs in the result set.
	// A missing policy (deleted after runs were created) is non-fatal.
	policyNames := make(map[string]string)
	for _, run := range rows {
		if _, seen := policyNames[run.PolicyID]; !seen {
			policyNames[run.PolicyID] = ""
		}
	}
	for pid := range policyNames {
		policy, err := h.store.GetPolicy(ctx, pid)
		if err == nil {
			policyNames[pid] = policy.Name
		} else if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("GetPolicy for run list failed", "policy_id", pid, "err", err)
		}
	}

	result := make([]RunSummary, 0, len(rows))
	for _, run := range rows {
		s := toRunSummary(run)
		s.PolicyName = policyNames[run.PolicyID]
		result = append(result, s)
	}

	api.WriteJSON(w, http.StatusOK, PaginatedRunsResponse{Runs: result, Total: total})
}

// Get handles GET /api/v1/runs/{runID}.
func (h *RunsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	run, err := h.store.GetRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		api.WriteError(w, http.StatusNotFound, "run not found", "")
		return
	}
	if err != nil {
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		api.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	summary := toRunSummary(run)

	// Fetch the associated policy name for the run detail view. A missing policy
	// (e.g. deleted after the run was created) is non-fatal — the frontend can
	// fall back to the policy_id.
	policy, err := h.store.GetPolicy(ctx, run.PolicyID)
	if err == nil {
		summary.PolicyName = policy.Name
	} else if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("GetPolicy for run detail failed", "policy_id", run.PolicyID, "err", err)
	}

	api.WriteJSON(w, http.StatusOK, summary)
}

// ListSteps handles GET /api/v1/runs/{runID}/steps.
func (h *RunsHandler) ListSteps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	// Guard: ListRunSteps returns an empty slice for a nonexistent run, so we
	// need a separate existence check to distinguish "no steps" from "no run".
	if _, err := h.store.GetRun(ctx, runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			api.WriteError(w, http.StatusNotFound, "run not found", "")
			return
		}
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		api.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	steps, err := h.store.ListRunSteps(ctx, runID)
	if err != nil {
		slog.Error("ListRunSteps query failed", "run_id", runID, "err", err)
		api.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	result := make([]StepSummary, 0, len(steps))
	for _, s := range steps {
		result = append(result, StepSummary{
			ID:         s.ID,
			RunID:      s.RunID,
			StepNumber: s.StepNumber,
			Type:       s.Type,
			Content:    s.Content,
			TokenCost:  s.TokenCost,
			CreatedAt:  s.CreatedAt,
		})
	}

	api.WriteJSON(w, http.StatusOK, result)
}

// Cancel handles POST /api/v1/runs/{runID}/cancel.
// It signals the run goroutine to stop; the goroutine itself transitions the
// run to failed in the DB.
func (h *RunsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")

	run, err := h.store.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			api.WriteError(w, http.StatusNotFound, "run not found", "")
			return
		}
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		api.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	if run.Status != string(model.RunStatusRunning) {
		api.WriteError(w, http.StatusConflict, "run is not in a cancellable state", run.Status)
		return
	}

	if !h.manager.Cancel(runID) {
		// Run is in running state in the DB but has no registered cancel func.
		// This can happen during the TOCTOU window where the goroutine completed
		// and deregistered itself between our GetRun check and this call.
		slog.Warn("cancel called for running run with no registered goroutine", "run_id", runID)
	}

	api.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

func toRunSummary(r db.Run) RunSummary {
	return RunSummary{
		ID:             r.ID,
		PolicyID:       r.PolicyID,
		Status:         r.Status,
		TriggerType:    r.TriggerType,
		TriggerPayload: r.TriggerPayload,
		StartedAt:      r.StartedAt,
		CompletedAt:    r.CompletedAt,
		TokenCost:      r.TokenCost,
		Error:          r.Error,
		CreatedAt:      r.CreatedAt,
		SystemPrompt:   r.SystemPrompt,
	}
}

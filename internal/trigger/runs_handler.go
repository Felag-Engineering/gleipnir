package trigger

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// RunSummary is the JSON shape returned for a single run.
type RunSummary struct {
	ID          string  `json:"id"`
	PolicyID    string  `json:"policy_id"`
	Status      string  `json:"status"`
	TriggerType string  `json:"trigger_type"`
	StartedAt   string  `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	TokenCost   int64   `json:"token_cost"`
	Error       *string `json:"error"`
	CreatedAt   string  `json:"created_at"`
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

// RunsHandler serves read-only run inspection endpoints.
type RunsHandler struct {
	store *db.Store
}

// NewRunsHandler returns a RunsHandler backed by store.
func NewRunsHandler(store *db.Store) *RunsHandler {
	return &RunsHandler{store: store}
}

// List handles GET /api/v1/runs with optional ?policy_id= and ?status= filters
// and ?limit= / ?offset= pagination.
func (h *RunsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var policyID interface{}
	if v := r.URL.Query().Get("policy_id"); v != "" {
		policyID = v
	}

	var status interface{}
	if v := r.URL.Query().Get("status"); v != "" {
		if !model.RunStatus(v).Valid() {
			http.Error(w, fmt.Sprintf("invalid status %q: must be one of pending, running, complete, failed, waiting_for_approval, interrupted", v), http.StatusBadRequest)
			return
		}
		status = v
	}

	limit := int64(50)
	if v := r.URL.Query().Get("limit"); v != "" {
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
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n >= 0 {
			offset = n
		}
	}

	rows, err := h.store.ListRuns(ctx, db.ListRunsParams{
		PolicyID: policyID,
		Status:   status,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		slog.Error("ListRuns query failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	result := make([]RunSummary, 0, len(rows))
	for _, run := range rows {
		result = append(result, toRunSummary(run))
	}

	writeJSON(w, result)
}

// Get handles GET /api/v1/runs/{runID}.
func (h *RunsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	run, err := h.store.GetRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, toRunSummary(run))
}

// ListSteps handles GET /api/v1/runs/{runID}/steps.
func (h *RunsHandler) ListSteps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	// Guard: ListRunSteps returns an empty slice for a nonexistent run, so we
	// need a separate existence check to distinguish "no steps" from "no run".
	if _, err := h.store.GetRun(ctx, runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	steps, err := h.store.ListRunSteps(ctx, runID)
	if err != nil {
		slog.Error("ListRunSteps query failed", "run_id", runID, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
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

	writeJSON(w, result)
}

func toRunSummary(r db.Run) RunSummary {
	return RunSummary{
		ID:          r.ID,
		PolicyID:    r.PolicyID,
		Status:      r.Status,
		TriggerType: r.TriggerType,
		StartedAt:   r.StartedAt,
		CompletedAt: r.CompletedAt,
		TokenCost:   r.TokenCost,
		Error:       r.Error,
		CreatedAt:   r.CreatedAt,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to encode JSON response", "err", err)
	}
}

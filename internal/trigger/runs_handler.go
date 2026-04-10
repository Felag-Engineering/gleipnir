package trigger

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/event"
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
	Model          string  `json:"model"`
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

// ApprovalDecisionRequest is the JSON body for SubmitApproval.
type ApprovalDecisionRequest struct {
	Decision string `json:"decision"` // "approved" or "denied"
}

// FeedbackDecisionRequest is the JSON body for SubmitFeedback.
type FeedbackDecisionRequest struct {
	Response string `json:"response"` // operator's freeform text
}

// RunsHandler serves run inspection and control endpoints.
type RunsHandler struct {
	store     *db.Store
	manager   *RunManager
	publisher event.Publisher
}

// NewRunsHandler returns a RunsHandler backed by store, manager, and publisher.
// publisher may be nil, in which case no SSE events are emitted.
func NewRunsHandler(store *db.Store, manager *RunManager, publisher event.Publisher) *RunsHandler {
	return &RunsHandler{store: store, manager: manager, publisher: publisher}
}

// List handles GET /api/v1/runs with optional filters and pagination.
// Query params: policy_id, status, since (RFC3339), until (RFC3339),
// sort ("started_at"|"started"|"duration"|"token_cost"), order ("asc"|"desc"), limit, offset.
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
			api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid status %q: must be one of pending, running, complete, failed, waiting_for_approval, waiting_for_feedback, interrupted", v), "")
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
		sort = "started_at"
	}
	// "started" is accepted as a backward-compatible alias for "started_at".
	switch sort {
	case "started_at", "started", "duration", "token_cost":
		// valid
	default:
		api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid sort %q: must be one of started_at, duration, token_cost", sort), "")
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

	limit := int64(25)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}

	offset := int64(0)
	if v := q.Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n >= 0 {
			offset = n
		}
	}

	filterBase := db.ListRunsParams{
		PolicyID: policyID,
		Status:   status,
		Since:    since,
		Until:    until,
		Limit:    limit,
		Offset:   offset,
	}

	var rows []db.Run
	var err error
	switch {
	case (sort == "started_at" || sort == "started") && order == "asc":
		rows, err = h.store.ListRunsAsc(ctx, db.ListRunsAscParams(filterBase))
	case (sort == "started_at" || sort == "started") && order == "desc":
		rows, err = h.store.ListRuns(ctx, filterBase)
	case sort == "token_cost" && order == "asc":
		rows, err = h.store.ListRunsByTokenCostAsc(ctx, db.ListRunsByTokenCostAscParams(filterBase))
	case sort == "token_cost" && order == "desc":
		rows, err = h.store.ListRunsByTokenCostDesc(ctx, db.ListRunsByTokenCostDescParams(filterBase))
	case sort == "duration" && order == "asc":
		rows, err = h.store.ListRunsByDurationAsc(ctx, db.ListRunsByDurationAscParams(filterBase))
	case sort == "duration" && order == "desc":
		rows, err = h.store.ListRunsByDurationDesc(ctx, db.ListRunsByDurationDescParams(filterBase))
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

	if _, err := h.store.GetRun(r.Context(), runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			api.WriteError(w, http.StatusNotFound, "run not found", "")
			return
		}
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		api.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	if err := h.manager.Cancel(runID); err != nil {
		api.WriteError(w, http.StatusConflict, "run is not in a cancellable state", "")
		return
	}

	api.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

// SubmitApproval handles POST /api/v1/runs/{runID}/approval.
// It routes the approval decision to the BoundAgent's approval gate via the
// RunManager. Returns 409 if no goroutine is waiting on the approval gate.
// The Approver role is required (enforced at the router level).
func (h *RunsHandler) SubmitApproval(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	var req ApprovalDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.Decision != "approved" && req.Decision != "denied" {
		api.WriteError(w, http.StatusBadRequest, `decision must be "approved" or "denied"`, req.Decision)
		return
	}

	if _, err := h.store.GetRun(ctx, runID); errors.Is(err, sql.ErrNoRows) {
		api.WriteError(w, http.StatusNotFound, "run not found", "")
		return
	} else if err != nil {
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		api.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	approved := req.Decision == "approved"
	if err := h.manager.SendApproval(runID, approved); err != nil {
		api.WriteError(w, http.StatusConflict, "no active approval gate for this run", "")
		return
	}

	// Map API decision to DB status: "denied" → "rejected" (model enum).
	dbStatus := string(model.ApprovalStatusApproved)
	if !approved {
		dbStatus = string(model.ApprovalStatusRejected)
	}

	// Update the pending approval_request record. Best-effort after the channel
	// send — DB consistency is secondary to unblocking the agent.
	pendingApprovals, err := h.store.GetPendingApprovalRequestsByRun(ctx, runID)
	if err != nil {
		slog.Warn("GetPendingApprovalRequestsByRun failed after approval send", "run_id", runID, "err", err)
	}

	var approvalID string
	if len(pendingApprovals) > 0 {
		approvalID = pendingApprovals[0].ID
		now := time.Now().UTC().Format(time.RFC3339Nano)
		rows, err := h.store.UpdateApprovalRequestStatus(ctx, db.UpdateApprovalRequestStatusParams{
			Status:    dbStatus,
			DecidedAt: &now,
			Note:      nil,
			ID:        approvalID,
		})
		if err != nil {
			slog.Warn("UpdateApprovalRequestStatus failed", "approval_id", approvalID, "err", err)
		} else if rows == 0 {
			// The scanner already resolved this request (e.g. timeout raced with the
			// operator's decision). Return 409 so the caller knows the action is too late.
			api.WriteError(w, http.StatusConflict, "approval request already resolved", approvalID)
			return
		}
	}

	if h.publisher != nil {
		if data, err := json.Marshal(map[string]string{
			"approval_id": approvalID,
			"run_id":      runID,
			"status":      dbStatus,
		}); err == nil {
			h.publisher.Publish("approval.resolved", data)
		}
	}

	api.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": runID, "decision": req.Decision})
}

// SubmitFeedback handles POST /api/v1/runs/{runID}/feedback.
// It routes the operator's freeform text response to the BoundAgent via the
// RunManager and updates the feedback_requests DB record. Returns 409 if no
// goroutine is waiting on the feedback gate.
func (h *RunsHandler) SubmitFeedback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	var req FeedbackDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.Response == "" {
		api.WriteError(w, http.StatusBadRequest, "response must not be empty", "")
		return
	}

	if _, err := h.store.GetRun(ctx, runID); errors.Is(err, sql.ErrNoRows) {
		api.WriteError(w, http.StatusNotFound, "run not found", "")
		return
	} else if err != nil {
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		api.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	if err := h.manager.SendFeedback(runID, req.Response); err != nil {
		api.WriteError(w, http.StatusConflict, "no active feedback gate for this run", "")
		return
	}

	// Update the pending feedback_request record. Best-effort after the channel
	// send — DB consistency is secondary to unblocking the agent.
	pendingFeedbacks, err := h.store.GetPendingFeedbackRequestsByRun(ctx, runID)
	if err != nil {
		slog.Warn("GetPendingFeedbackRequestsByRun failed after feedback send", "run_id", runID, "err", err)
	}

	var feedbackID string
	if len(pendingFeedbacks) > 0 {
		feedbackID = pendingFeedbacks[0].ID
		now := time.Now().UTC().Format(time.RFC3339Nano)
		rows, err := h.store.UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
			Status:     "resolved",
			Response:   &req.Response,
			ResolvedAt: &now,
			ID:         feedbackID,
		})
		if err != nil {
			slog.Warn("UpdateFeedbackRequestStatus failed", "feedback_id", feedbackID, "err", err)
		} else if rows == 0 {
			// The scanner already resolved this request (timeout raced with the
			// operator's response). Return 409 so the caller knows the action is too late.
			api.WriteError(w, http.StatusConflict, "feedback request already resolved", feedbackID)
			return
		}
	}

	if h.publisher != nil {
		if data, err := json.Marshal(map[string]string{
			"feedback_id": feedbackID,
			"run_id":      runID,
		}); err == nil {
			h.publisher.Publish("feedback.resolved", data)
		}
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
		Model:          r.Model,
	}
}

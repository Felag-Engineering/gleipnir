package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
)

const eventTypeFeedbackResponseLate = "feedback_response_late"

// PaginatedRunsResponse is the JSON envelope returned by List.
type PaginatedRunsResponse struct {
	Runs  []RunSummary `json:"runs"`
	Total int64        `json:"total"`
}

// RunSummary is the JSON shape returned for a single run.
type RunSummary struct {
	ID                string  `json:"id"`
	PolicyID          string  `json:"policy_id"`
	PolicyName        string  `json:"policy_name"`
	Status            string  `json:"status"`
	TriggerType       string  `json:"trigger_type"`
	TriggerPayload    string  `json:"trigger_payload"`
	StartedAt         string  `json:"started_at"`
	CompletedAt       *string `json:"completed_at"`
	TokenCost         int64   `json:"token_cost"`
	Error             *string `json:"error"`
	CreatedAt         string  `json:"created_at"`
	SystemPrompt      *string `json:"system_prompt"`
	Model             string  `json:"model"`
	ApprovalExpiresAt *string `json:"approval_expires_at,omitempty"` // omitted from list responses; set only by Get when waiting_for_approval
	PolicyUpdatedAt   *string `json:"policy_updated_at,omitempty"`   // omitted from list responses; set only by Get
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

// listFilters holds the parsed and validated query parameters for List.
type listFilters struct {
	policyID interface{} // nil when absent; string when set
	status   interface{} // nil when absent; validated string otherwise
	since    interface{} // nil when absent; RFC3339-validated string otherwise
	until    interface{} // nil when absent; RFC3339-validated string otherwise
	sort     string      // canonicalized: "started_at" | "duration" | "token_cost"
	order    string      // "asc" | "desc"
	limit    int64       // clamped to [1, 100]; default 25
	offset   int64       // >= 0; default 0
}

// httpError carries the (status, msg, detail) triple that parseListFilters
// needs to communicate to the caller for each validation failure.
type httpError struct {
	status int
	msg    string
	detail string
}

// sortKey is the composite key for the listRunsDispatch map.
type sortKey struct{ sort, order string }

// listRunsDispatch maps a (sort, order) pair to the store method that
// implements it. Built once at package init; each closure converts the
// canonical ListRunsParams to the concrete *Params type required by sqlc.
var listRunsDispatch = map[sortKey]func(ctx context.Context, store *db.Store, p db.ListRunsParams) ([]db.Run, error){
	{"started_at", "asc"}: func(ctx context.Context, s *db.Store, p db.ListRunsParams) ([]db.Run, error) {
		return s.ListRunsAsc(ctx, db.ListRunsAscParams(p))
	},
	{"started_at", "desc"}: func(ctx context.Context, s *db.Store, p db.ListRunsParams) ([]db.Run, error) {
		return s.ListRuns(ctx, p)
	},
	{"token_cost", "asc"}: func(ctx context.Context, s *db.Store, p db.ListRunsParams) ([]db.Run, error) {
		return s.ListRunsByTokenCostAsc(ctx, db.ListRunsByTokenCostAscParams(p))
	},
	{"token_cost", "desc"}: func(ctx context.Context, s *db.Store, p db.ListRunsParams) ([]db.Run, error) {
		return s.ListRunsByTokenCostDesc(ctx, db.ListRunsByTokenCostDescParams(p))
	},
	{"duration", "asc"}: func(ctx context.Context, s *db.Store, p db.ListRunsParams) ([]db.Run, error) {
		return s.ListRunsByDurationAsc(ctx, db.ListRunsByDurationAscParams(p))
	},
	{"duration", "desc"}: func(ctx context.Context, s *db.Store, p db.ListRunsParams) ([]db.Run, error) {
		return s.ListRunsByDurationDesc(ctx, db.ListRunsByDurationDescParams(p))
	},
}

// parseListFilters reads and validates query parameters from r, returning the
// parsed filters or an *httpError describing the first validation failure.
func parseListFilters(r *http.Request) (listFilters, *httpError) {
	q := r.URL.Query()
	var f listFilters

	if v := q.Get("policy_id"); v != "" {
		f.policyID = v
	}

	if v := q.Get("status"); v != "" {
		if !model.RunStatus(v).Valid() {
			return f, &httpError{
				status: http.StatusBadRequest,
				msg:    fmt.Sprintf("invalid status %q: must be one of pending, running, complete, failed, waiting_for_approval, waiting_for_feedback, interrupted", v),
			}
		}
		f.status = v
	}

	if v := q.Get("since"); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return f, &httpError{
				status: http.StatusBadRequest,
				msg:    fmt.Sprintf("invalid since %q: must be RFC3339", v),
			}
		}
		f.since = v
	}

	if v := q.Get("until"); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return f, &httpError{
				status: http.StatusBadRequest,
				msg:    fmt.Sprintf("invalid until %q: must be RFC3339", v),
			}
		}
		f.until = v
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
		return f, &httpError{
			status: http.StatusBadRequest,
			msg:    fmt.Sprintf("invalid sort %q: must be one of started_at, duration, token_cost", sort),
		}
	}
	// Normalize the alias after validation so the dispatch map only needs three sort keys.
	if sort == "started" {
		sort = "started_at"
	}
	f.sort = sort

	order := q.Get("order")
	if order == "" {
		order = "desc"
	}
	if order != "asc" && order != "desc" {
		return f, &httpError{
			status: http.StatusBadRequest,
			msg:    fmt.Sprintf("invalid order %q: must be \"asc\" or \"desc\"", order),
		}
	}
	f.order = order

	f.limit = int64(25)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			f.limit = n
		}
	}
	if f.limit < 1 {
		f.limit = 25
	}
	if f.limit > 100 {
		f.limit = 100
	}

	f.offset = int64(0)
	if v := q.Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n >= 0 {
			f.offset = n
		}
	}

	return f, nil
}

// List handles GET /api/v1/runs with optional filters and pagination.
// Query params: policy_id, status, since (RFC3339), until (RFC3339),
// sort ("started_at"|"started"|"duration"|"token_cost"), order ("asc"|"desc"), limit, offset.
func (h *RunsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	filters, herr := parseListFilters(r)
	if herr != nil {
		httputil.WriteError(w, herr.status, herr.msg, herr.detail)
		return
	}

	filterBase := db.ListRunsParams{
		PolicyID: filters.policyID,
		Status:   filters.status,
		Since:    filters.since,
		Until:    filters.until,
		Limit:    filters.limit,
		Offset:   filters.offset,
	}

	dispatch, ok := listRunsDispatch[sortKey{filters.sort, filters.order}]
	if !ok {
		// Should never happen: parseListFilters normalizes sort and validates order.
		slog.Error("listRunsDispatch: no entry for sort/order pair", "sort", filters.sort, "order", filters.order)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	rows, err := dispatch(ctx, h.store, filterBase)
	if err != nil {
		slog.Error("ListRuns query failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	total, err := h.store.CountRuns(ctx, db.CountRunsParams{
		PolicyID: filters.policyID,
		Status:   filters.status,
		Since:    filters.since,
		Until:    filters.until,
	})
	if err != nil {
		slog.Error("CountRuns query failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
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

	httputil.WriteJSON(w, http.StatusOK, PaginatedRunsResponse{Runs: result, Total: total})
}

// Get handles GET /api/v1/runs/{runID}.
func (h *RunsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	run, err := h.store.GetRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		httputil.WriteError(w, http.StatusNotFound, "run not found", "")
		return
	}
	if err != nil {
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	summary := toRunSummary(run)

	// Fetch the associated policy name for the run detail view. A missing policy
	// (e.g. deleted after the run was created) is non-fatal — the frontend can
	// fall back to the policy_id.
	policy, err := h.store.GetPolicy(ctx, run.PolicyID)
	if err == nil {
		summary.PolicyName = policy.Name
		summary.PolicyUpdatedAt = &policy.UpdatedAt
	} else if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("GetPolicy for run detail failed", "policy_id", run.PolicyID, "err", err)
	}

	// Populate approval_expires_at only for runs actively waiting for approval.
	// Best-effort: a query failure must never cause the Get response to fail.
	if run.Status == string(model.RunStatusWaitingForApproval) {
		pending, err := h.store.GetPendingApprovalRequestsByRun(ctx, runID)
		if err != nil {
			slog.Warn("GetPendingApprovalRequestsByRun for run detail failed", "run_id", runID, "err", err)
		} else if len(pending) > 0 {
			summary.ApprovalExpiresAt = &pending[0].ExpiresAt
		}
	}

	httputil.WriteJSON(w, http.StatusOK, summary)
}

// defaultStepsLimit is the number of steps returned when the client does not
// specify a limit. It keeps the initial page fast while covering most runs.
const defaultStepsLimit = int64(500)

// maxStepsLimit caps the limit a client may request in a single call. This
// prevents a single request from pulling an arbitrarily large result set.
const maxStepsLimit = int64(1000)

// ListSteps handles GET /api/v1/runs/{runID}/steps.
// Query params:
//   - after  — step_number cursor (exclusive); any value < 0 means "from the beginning". Default: -1.
//   - limit  — max steps to return; clamped to [1, 1000]. Default: 500.
func (h *RunsHandler) ListSteps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	q := r.URL.Query()

	// Parse the cursor. Any absent, unparsable, or negative value defaults to -1
	// (from beginning), matching the sentinel used in the SQL query.
	after := int64(-1)
	if v := q.Get("after"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			after = n
		}
	}

	// Parse and clamp limit.
	limit := defaultStepsLimit
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = defaultStepsLimit
	}
	if limit > maxStepsLimit {
		limit = maxStepsLimit
	}

	// Guard: ListRunSteps returns an empty slice for a nonexistent run, so we
	// need a separate existence check to distinguish "no steps" from "no run".
	if _, err := h.store.GetRun(ctx, runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "run not found", "")
			return
		}
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	steps, err := h.store.ListRunSteps(ctx, db.ListRunStepsParams{
		RunID: runID,
		After: after,
		Limit: limit,
	})
	if err != nil {
		slog.Error("ListRunSteps query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
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

	httputil.WriteJSON(w, http.StatusOK, result)
}

// Cancel handles POST /api/v1/runs/{runID}/cancel.
// It signals the run goroutine to stop; the goroutine itself transitions the
// run to failed in the DB.
func (h *RunsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")

	if _, err := h.store.GetRun(r.Context(), runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "run not found", "")
			return
		}
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	if err := h.manager.Cancel(runID); err != nil {
		httputil.WriteError(w, http.StatusConflict, "run is not in a cancellable state", "")
		return
	}

	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

// resolveSpec carries the request-kind-specific behaviour for resolveRequest.
// Approval and feedback share identical orchestration; only the data differs.
type resolveSpec struct {
	sendToGate         func() error
	gateErrorMsg       string
	fetchPending       func(ctx context.Context) (string, error)
	updateStatus       func(ctx context.Context, requestID string) (int64, error)
	alreadyResolvedMsg string
	sseTopic           string
	sseRequestIDKey    string            // "approval_id" or "feedback_id"
	sseExtra           map[string]string // merged into the SSE payload alongside run_id and sseRequestIDKey
	successResponse    map[string]string
	logTagPending      string
	logTagUpdate       string
}

// resolveRequest is the shared orchestration path for SubmitApproval and
// SubmitFeedback. It executes: GetRun → sendToGate → fetchPending →
// updateStatus → SSE publish → 202 response.
//
// updateStatus is called only when fetchPending returns a non-empty requestID.
// SSE publish errors are silently swallowed to match the pre-refactor behaviour.
// resolveRequest intentionally does NOT call any runstate.* function; the runs
// table transition out of waiting_for_* is performed by the agent goroutine
// after receiving on the channel (ADR-038).
func (h *RunsHandler) resolveRequest(w http.ResponseWriter, r *http.Request, runID string, spec resolveSpec) {
	ctx := r.Context()

	if _, err := h.store.GetRun(ctx, runID); errors.Is(err, sql.ErrNoRows) {
		httputil.WriteError(w, http.StatusNotFound, "run not found", "")
		return
	} else if err != nil {
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	if err := spec.sendToGate(); err != nil {
		httputil.WriteError(w, http.StatusConflict, spec.gateErrorMsg, "")
		return
	}

	// Update the pending request record. Best-effort after the channel send —
	// DB consistency is secondary to unblocking the agent.
	requestID, err := spec.fetchPending(ctx)
	if err != nil {
		slog.Warn(spec.logTagPending, "run_id", runID, "err", err)
	}

	if requestID != "" {
		rows, err := spec.updateStatus(ctx, requestID)
		if err != nil {
			slog.Warn(spec.logTagUpdate, spec.sseRequestIDKey, requestID, "run_id", runID, "err", err)
			// proceed — best-effort semantics match the pre-refactor code
		} else if rows == 0 {
			// The scanner already resolved this request (e.g. timeout raced with
			// the operator's decision). Return 409 so the caller knows it's too late.
			httputil.WriteError(w, http.StatusConflict, spec.alreadyResolvedMsg, requestID)
			return
		}
	}

	if h.publisher != nil {
		payload := map[string]string{
			spec.sseRequestIDKey: requestID,
			"run_id":             runID,
		}
		for k, v := range spec.sseExtra {
			payload[k] = v
		}
		if data, err := json.Marshal(payload); err == nil {
			h.publisher.Publish(spec.sseTopic, data)
		}
		// marshal errors silently ignored — matches pre-refactor behaviour
	}

	httputil.WriteJSON(w, http.StatusAccepted, spec.successResponse)
}

// SubmitApproval handles POST /api/v1/runs/{runID}/approval.
// It routes the approval decision to the BoundAgent's approval gate via the
// RunManager. Returns 409 if no goroutine is waiting on the approval gate.
// The Approver role is required (enforced at the router level).
func (h *RunsHandler) SubmitApproval(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")

	var req ApprovalDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.Decision != "approved" && req.Decision != "denied" {
		httputil.WriteError(w, http.StatusBadRequest, `decision must be "approved" or "denied"`, req.Decision)
		return
	}

	approved := req.Decision == "approved"

	// Map API decision to DB status: "denied" → "rejected" (model enum).
	dbStatus := string(model.ApprovalStatusApproved)
	if !approved {
		dbStatus = string(model.ApprovalStatusRejected)
	}

	h.resolveRequest(w, r, runID, resolveSpec{
		sendToGate:   func() error { return h.manager.SendApproval(runID, approved) },
		gateErrorMsg: "no active approval gate for this run",
		fetchPending: func(ctx context.Context) (string, error) {
			pendingApprovals, err := h.store.GetPendingApprovalRequestsByRun(ctx, runID)
			if err != nil {
				return "", err
			}
			if len(pendingApprovals) > 0 {
				return pendingApprovals[0].ID, nil
			}
			return "", nil
		},
		updateStatus: func(ctx context.Context, requestID string) (int64, error) {
			// now must be evaluated here, at DB-write time, not captured at spec-construction time.
			now := time.Now().UTC().Format(time.RFC3339Nano)
			return h.store.UpdateApprovalRequestStatus(ctx, db.UpdateApprovalRequestStatusParams{
				Status:    dbStatus,
				DecidedAt: &now,
				Note:      nil,
				ID:        requestID,
			})
		},
		alreadyResolvedMsg: "approval request already resolved",
		sseTopic:           "approval.resolved",
		sseRequestIDKey:    "approval_id",
		sseExtra:           map[string]string{"status": dbStatus},
		successResponse:    map[string]string{"run_id": runID, "decision": req.Decision},
		logTagPending:      "GetPendingApprovalRequestsByRun failed after approval send",
		logTagUpdate:       "UpdateApprovalRequestStatus failed",
	})
}

// SubmitFeedback handles POST /api/v1/runs/{runID}/feedback.
// It delivers the operator's freeform text response through inAppChannel.Resolve
// via RunManager.ResolveFeedback, then updates the feedback_requests DB record
// and emits an SSE event. Returns 409 if no pending feedback row exists, 410 if
// the waiter has already timed out or been answered.
func (h *RunsHandler) SubmitFeedback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	// (a) Validate body.
	var req FeedbackDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.Response == "" {
		httputil.WriteError(w, http.StatusBadRequest, "response must not be empty", "")
		return
	}

	if _, err := h.store.GetRun(ctx, runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "run not found", "")
			return
		}
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	// (b) Look up pending feedback row. Zero rows means no active feedback gate.
	// Per key decision #8: inAppChannel.Request registers the waiter BEFORE
	// sm.Transition writes the DB row, so by the time the run is observable as
	// waiting_for_feedback, the row exists. Zero rows genuinely means no waiter.
	pendingFeedbacks, err := h.store.GetPendingFeedbackRequestsByRun(ctx, runID)
	if err != nil {
		slog.Error("GetPendingFeedbackRequestsByRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}
	if len(pendingFeedbacks) == 0 {
		httputil.WriteError(w, http.StatusConflict, "no active feedback gate for this run", "")
		return
	}
	pendingID := pendingFeedbacks[0].ID

	// (c) Deliver response through the inAppChannel waiter map.
	if err := h.manager.ResolveFeedback(runID, pendingID, req.Response); err != nil {
		switch {
		case errors.Is(err, ErrRunNotFound):
			httputil.WriteError(w, http.StatusConflict, "no active feedback gate for this run", "")
			return
		case errors.Is(err, agent.ErrUnknownRequestID):
			// The waiter has already expired or been answered (e.g. the feedback-timeout
			// scanner resolved it between step (b) and step (c)). This is a benign
			// late-callback; log for observability but never log the body itself.
			slog.Warn("feedback_response_late",
				"request_id", pendingID,
				"run_id", runID,
				"body_len", len(req.Response))

			// Best-effort: write an audit row per ADR-046. Never include the response body.
			auditPayload, marshalErr := json.Marshal(map[string]string{
				"request_id": pendingID,
				"run_id":     runID,
				"substrate":  "in_app",
				"reason":     "late",
			})
			if marshalErr != nil {
				slog.Warn("feedback_response_late: audit payload marshal failed",
					"request_id", pendingID,
					"run_id", runID,
					"err", marshalErr)
			} else {
				if _, insertErr := h.store.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
					PluginInstanceID: nil,
					EventType:        eventTypeFeedbackResponseLate,
					Severity:         "warning",
					ActorUserID:      nil,
					PayloadJson:      string(auditPayload),
					CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
				}); insertErr != nil {
					slog.Warn("feedback_response_late: audit event insert failed",
						"request_id", pendingID,
						"run_id", runID,
						"err", insertErr)
				}
			}

			httputil.WriteError(w, http.StatusGone, "feedback request expired or already answered", "")
			return
		default:
			slog.Error("ResolveFeedback failed", "run_id", runID, "request_id", pendingID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
			return
		}
	}

	// (d) Update the DB record. Best-effort after the channel delivery — DB
	// consistency is secondary to unblocking the agent.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := h.store.UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
		Status:     "resolved",
		Response:   &req.Response,
		ResolvedAt: &now,
		ID:         pendingID,
	})
	if err != nil {
		slog.Warn("UpdateFeedbackRequestStatus failed", "feedback_id", pendingID, "run_id", runID, "err", err)
		// proceed — best-effort; agent has already resumed
	} else if rows == 0 {
		// Benign two-writer race: the feedback-timeout scanner can resolve this row
		// between (b) and (d). Resolve in (c) still succeeded against the live waiter
		// map, so the agent has already resumed; only this HTTP caller sees 409.
		// See plan key decision #7.
		httputil.WriteError(w, http.StatusConflict, "feedback request already resolved", pendingID)
		return
	}

	// (e) Emit SSE feedback.resolved.
	if h.publisher != nil {
		payload := map[string]string{
			"feedback_id": pendingID,
			"run_id":      runID,
		}
		if data, err := json.Marshal(payload); err == nil {
			h.publisher.Publish("feedback.resolved", data)
		}
	}

	// (f) Return 202.
	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": runID})
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

package run

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/go-chi/chi/v5"
)

// UserRef identifies the person behind a decision. The username is looked up
// at read time so a rename shows up on old runs; a deleted account has no
// reference at all (decided_by is SET NULL on delete).
type UserRef struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// ResponderSummary says who settled one approval or feedback request of a run.
// DecidedBy is nil when the system settled it (timeout), when the row predates
// the decided_by column, or when the deciding account was deleted.
type ResponderSummary struct {
	RequestID string   `json:"request_id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	DecidedAt *string  `json:"decided_at"`
	DecidedBy *UserRef `json:"decided_by"`
}

// userRefOf builds a UserRef from the nullable id/username pair of a LEFT JOIN
// against users. A non-null id with a null username cannot happen under the
// foreign key, but returns nil rather than a half-filled reference.
func userRefOf(id, username *string) *UserRef {
	if id == nil || username == nil {
		return nil
	}
	return &UserRef{ID: *id, Username: *username}
}

// ListResponders handles GET /api/v1/runs/{runID}/responders: who settled each
// of the run's approval and feedback requests. A separate endpoint from /steps
// because /steps is the trace the model is replayed and the identity of the
// person who answered is operator-facing evidence, not model context (ADR-046).
func (h *RunsHandler) ListResponders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	if _, err := h.store.GetRun(ctx, runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "run not found", "")
			return
		}
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	approvals, err := h.store.ListApprovalDecidersByRun(ctx, runID)
	if err != nil {
		slog.Error("ListApprovalDecidersByRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}
	feedback, err := h.store.ListFeedbackRespondersByRun(ctx, runID)
	if err != nil {
		slog.Error("ListFeedbackRespondersByRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	result := make([]ResponderSummary, 0, len(approvals)+len(feedback))
	for _, a := range approvals {
		result = append(result, ResponderSummary{
			RequestID: a.ID,
			Kind:      "approval",
			Status:    a.Status,
			DecidedAt: a.DecidedAt,
			DecidedBy: userRefOf(a.DecidedBy, a.DecidedByUsername),
		})
	}
	for _, f := range feedback {
		result = append(result, ResponderSummary{
			RequestID: f.ID,
			Kind:      "feedback",
			Status:    f.Status,
			DecidedAt: f.ResolvedAt,
			DecidedBy: userRefOf(f.RespondedBy, f.RespondedByUsername),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

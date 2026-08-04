// Package run — this file holds the operator-facing side of tool-initiated
// HITL (ADR-055, mcp-realignment-spec.md §6.1): reading what a paused run is
// asking for, and answering it.
//
// The role gate here cannot be a route-level RequireRole, and that is the
// point. Which role may answer depends on what is being asked — a consent-only
// ask is an authorization decision needing an approver, a request for values is
// operating work needing an operator — and that is a property of the row, not
// of the endpoint. The route middleware narrows to "approver or operator" so an
// auditor never reaches the handler; the handler makes the real decision.
package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// ToolInputRequestResponse is the JSON shape returned for a pending
// tool-initiated input request.
type ToolInputRequestResponse struct {
	ID              string              `json:"id"`
	RunID           string              `json:"run_id"`
	ToolName        string              `json:"tool_name"`
	ElicitationKind string              `json:"elicitation_kind"`
	RequiredRole    string              `json:"required_role"`
	ExpiresAt       string              `json:"expires_at"`
	CreatedAt       string              `json:"created_at"`
	Requests        []ToolInputQuestion `json:"requests"`

	// UntrustedContent is always true and is part of the contract, not a
	// runtime condition (spec §6.1: elicitation messages are server-controlled
	// text). It is stated explicitly so a client cannot render these strings as
	// markup or as instructions by mistaking them for host-authored copy.
	UntrustedContent bool `json:"untrusted_content"`
}

// ToolInputQuestion is one elicitation inside a pending request.
type ToolInputQuestion struct {
	// Message is server-controlled text. Render as plain content only.
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requested_schema,omitempty"`
}

// ToolInputDecisionRequest is the body of a resolution. Responses are
// correlated to the pending request's questions by position — MRTR carries no
// per-question id — so the count must match exactly.
type ToolInputDecisionRequest struct {
	Responses []ToolInputResponseItem `json:"responses"`
}

// ToolInputResponseItem is one answer: an action plus, for "accept", the
// operator's values.
type ToolInputResponseItem struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
}

// GetToolInput returns the run's pending tool-initiated input request.
//
// Read access matches the rest of the run detail surface (operator, approver,
// auditor): seeing what a run is blocked on is not the same authority as
// answering it, and an auditor who cannot see the question cannot audit the
// decision.
func (h *RunsHandler) GetToolInput(w http.ResponseWriter, r *http.Request) {
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

	row, ok := h.pendingToolInput(w, ctx, runID)
	if !ok {
		return
	}

	questions, err := agent.DecodeInputRequestPayload(row.RequestPayload)
	if err != nil {
		// A row that cannot be rendered is a corrupt row, not an empty one:
		// answering it blind would be worse than reporting the failure.
		slog.Error("tool input request payload does not decode",
			"request_id", row.ID, "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	out := make([]ToolInputQuestion, len(questions))
	for i, q := range questions {
		out[i] = ToolInputQuestion{Message: q.Message, RequestedSchema: q.RequestedSchema}
	}

	kind := model.ElicitationKind(row.ElicitationKind)
	httputil.WriteJSON(w, http.StatusOK, ToolInputRequestResponse{
		ID:               row.ID,
		RunID:            row.RunID,
		ToolName:         row.ToolName,
		ElicitationKind:  row.ElicitationKind,
		RequiredRole:     kind.RequiredRole().String(),
		ExpiresAt:        row.ExpiresAt,
		CreatedAt:        row.CreatedAt,
		Requests:         out,
		UntrustedContent: true,
	})
}

// SubmitToolInput delivers an operator's answer to a paused tool-initiated
// input request.
//
// The DB row is deliberately NOT transitioned here. The agent goroutine that
// receives the answer records it (agent.InputRequiredHandler.Route), and having
// both sides CAS the same row would recreate the two-writer race the feedback
// path documents: whichever writer loses sees rows == 0, and this handler would
// report a conflict for an answer that was genuinely delivered.
func (h *RunsHandler) SubmitToolInput(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	var req ToolInputDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if len(req.Responses) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "responses must not be empty", "")
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

	row, ok := h.pendingToolInput(w, ctx, runID)
	if !ok {
		return
	}

	// The row-level role gate (spec §6.1). Read the kind from the persisted
	// row, never from the request body — the client does not get to tell the
	// host which authority its answer needs.
	kind := model.ElicitationKind(row.ElicitationKind)
	user, found := auth.UserFromContext(ctx)
	if !found {
		httputil.WriteError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}
	required := kind.RequiredRole()
	if !user.HasRole(model.RoleAdmin) && !user.HasRole(required) {
		httputil.WriteError(w, http.StatusForbidden, "insufficient permissions",
			"a "+row.ElicitationKind+" request is resolvable by the "+required.String()+" role")
		return
	}

	body, err := json.Marshal(req.Responses)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	// Delivery validates the answer against the pause it claims to answer
	// (action vocabulary, count, accept-carries-content). A rejection there is
	// the caller's problem to fix and leaves the run paused and answerable, so
	// it is a 400, not a 500.
	switch err := h.manager.ResolveToolInput(runID, row.ID, string(body)); {
	case err == nil:
	case errors.Is(err, ErrRunNotFound):
		httputil.WriteError(w, http.StatusConflict, "no active tool input gate for this run", "")
		return
	case errors.Is(err, agent.ErrUnknownInputRequestID):
		// Benign late callback: the wait timed out or was already answered
		// between the row read and the delivery. Never log the body.
		slog.Warn("tool_input_response_late", "request_id", row.ID, "run_id", runID)
		httputil.WriteError(w, http.StatusGone, "tool input request expired or already answered", "")
		return
	default:
		httputil.WriteError(w, http.StatusBadRequest, "invalid tool input response", err.Error())
		return
	}

	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": runID, "request_id": row.ID})
}

// pendingToolInput loads the run's single pending request, writing the error
// response itself and returning ok=false when there is none. A run pauses on
// one request at a time, so the first row is the one being answered.
func (h *RunsHandler) pendingToolInput(w http.ResponseWriter, ctx context.Context, runID string) (db.ToolInputRequest, bool) {
	rows, err := h.store.GetPendingToolInputRequestsByRun(ctx, runID)
	if err != nil {
		slog.Error("GetPendingToolInputRequestsByRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return db.ToolInputRequest{}, false
	}
	if len(rows) == 0 {
		httputil.WriteError(w, http.StatusNotFound, "no pending tool input request for this run", "")
		return db.ToolInputRequest{}, false
	}
	return rows[0], true
}

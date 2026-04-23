package api

import (
	"net/http"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/httputil"
)

// AttentionHandler serves GET /api/v1/attention.
type AttentionHandler struct {
	store *db.Store
}

// NewAttentionHandler creates an AttentionHandler backed by the given store.
func NewAttentionHandler(store *db.Store) *AttentionHandler {
	return &AttentionHandler{store: store}
}

// AttentionItem is one entry in the operator's attention queue.
// Type is "approval", "feedback", or "failure".
type AttentionItem struct {
	Type       string  `json:"type"`
	RequestID  string  `json:"request_id"`
	RunID      string  `json:"run_id"`
	PolicyID   string  `json:"policy_id"`
	PolicyName string  `json:"policy_name"`
	ToolName   string  `json:"tool_name"`
	Message    string  `json:"message"`
	ExpiresAt  *string `json:"expires_at"`
	CreatedAt  string  `json:"created_at"`
}

// AttentionResponse wraps the items list returned by GET /api/v1/attention.
type AttentionResponse struct {
	Items []AttentionItem `json:"items"`
}

// Get handles GET /api/v1/attention.
// It returns pending approval requests, pending feedback requests, and
// runs that failed in the last 24 hours. The three sources are merged into
// one list so the frontend can render a single attention queue.
func (h *AttentionHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	dbItems, err := h.store.ListAttentionItems(ctx)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list attention items", err.Error())
		return
	}

	// Also include runs that failed within the last 24 hours.
	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	failedStatus := "failed"
	failedRuns, err := h.store.ListRuns(ctx, db.ListRunsParams{
		Status: &failedStatus,
		Since:  &since,
		Limit:  10,
		Offset: 0,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list failed runs", err.Error())
		return
	}

	// Map pending approvals and feedback to AttentionItem.
	items := make([]AttentionItem, 0, len(dbItems)+len(failedRuns))
	for _, row := range dbItems {
		var expiresAt *string
		if row.ExpiresAt != "" {
			v := row.ExpiresAt
			expiresAt = &v
		}
		item := AttentionItem{
			Type:       row.ItemType,
			RequestID:  row.RequestID,
			RunID:      row.RunID,
			PolicyID:   row.PolicyID,
			PolicyName: row.PolicyName,
			ToolName:   row.ToolName,
			Message:    row.Message,
			ExpiresAt:  expiresAt,
			CreatedAt:  row.CreatedAt,
		}
		items = append(items, item)
	}

	// Map each failed run to an attention item. PolicyName comes from the run's
	// policy_id — we do a separate lookup so that the failure items have names.
	// The attention query already joins policies for approvals/feedback; for
	// failures we look up individually (at most 10 runs, so N+1 is acceptable).
	for _, run := range failedRuns {
		policyName := run.PolicyID // fallback if lookup fails
		if pol, err := h.store.GetPolicy(ctx, run.PolicyID); err == nil {
			policyName = pol.Name
		}

		var errMsg string
		if run.Error != nil {
			errMsg = *run.Error
		}

		items = append(items, AttentionItem{
			Type:       "failure",
			RequestID:  "",
			RunID:      run.ID,
			PolicyID:   run.PolicyID,
			PolicyName: policyName,
			ToolName:   "",
			Message:    errMsg,
			ExpiresAt:  nil,
			CreatedAt:  run.CreatedAt,
		})
	}

	httputil.WriteJSON(w, http.StatusOK, AttentionResponse{Items: items})
}

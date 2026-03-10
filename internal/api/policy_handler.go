package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/rapp992/gleipnir/internal/db"
)

// PolicyHandler serves GET /api/v1/policies and GET /api/v1/policies/{id}.
type PolicyHandler struct {
	store *db.Store
}

// NewPolicyHandler creates a PolicyHandler backed by the given store.
func NewPolicyHandler(store *db.Store) *PolicyHandler {
	return &PolicyHandler{store: store}
}

type runSummary struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	TokenCost int64  `json:"token_cost"`
}

type policyListItem struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	TriggerType string      `json:"trigger_type"`
	Folder      string      `json:"folder"`
	CreatedAt   string      `json:"created_at"`
	UpdatedAt   string      `json:"updated_at"`
	LatestRun   *runSummary `json:"latest_run"`
}

type policyDetail struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TriggerType string `json:"trigger_type"`
	Folder      string `json:"folder"`
	YAML        string `json:"yaml"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// List handles GET /api/v1/policies.
func (h *PolicyHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListPoliciesWithLatestRun(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list policies", err.Error())
		return
	}

	items := make([]policyListItem, 0, len(rows))
	for _, row := range rows {
		item := policyListItem{
			ID:          row.ID,
			Name:        row.Name,
			TriggerType: row.TriggerType,
			Folder:      extractFolder(row.Yaml),
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
		}

		// Build latest_run only when the LEFT JOIN matched a run row.
		// RunID is nil when no run exists (emit_pointers_for_null_types: true).
		if row.RunID != nil {
			item.LatestRun = &runSummary{
				ID:        *row.RunID,
				Status:    *row.RunStatus,
				StartedAt: *row.RunStartedAt,
				TokenCost: *row.RunTokenCost,
			}
		}

		items = append(items, item)
	}

	WriteJSON(w, http.StatusOK, items)
}

// Get handles GET /api/v1/policies/{id}.
func (h *PolicyHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	policy, err := h.store.GetPolicy(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to get policy", err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, policyDetail{
		ID:          policy.ID,
		Name:        policy.Name,
		TriggerType: policy.TriggerType,
		Folder:      extractFolder(policy.Yaml),
		YAML:        policy.Yaml,
		CreatedAt:   policy.CreatedAt,
		UpdatedAt:   policy.UpdatedAt,
	})
}

// extractFolder parses the folder field from a raw policy YAML blob.
// Folder is cosmetic/UI-only (ADR-020); returns "" on parse failure.
func extractFolder(rawYAML string) string {
	var v struct {
		Folder string `yaml:"folder"`
	}
	_ = yaml.Unmarshal([]byte(rawYAML), &v)
	return v.Folder
}

package api

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/policy"
)

// PolicyHandler serves policy CRUD endpoints under /api/v1/policies.
type PolicyHandler struct {
	store *db.Store
	svc   *policy.Service
}

// NewPolicyHandler creates a PolicyHandler backed by the given store and service.
func NewPolicyHandler(store *db.Store, svc *policy.Service) *PolicyHandler {
	return &PolicyHandler{store: store, svc: svc}
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
	PausedAt    *string     `json:"paused_at"`
	LatestRun   *runSummary `json:"latest_run"`
}

type policyDetail struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	TriggerType string  `json:"trigger_type"`
	Folder      string  `json:"folder"`
	YAML        string  `json:"yaml"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	PausedAt    *string `json:"paused_at"`
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
			PausedAt:    row.PausedAt,
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
		PausedAt:    policy.PausedAt,
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

// policyMutateResponse is the response body for Create and Update, extending
// policyDetail with a Warnings array for non-blocking MCP tool reference issues.
type policyMutateResponse struct {
	policyDetail
	Warnings []string `json:"warnings"`
}

// buildMutateResponse constructs a policyMutateResponse from a policy.SaveResult.
// Warnings is always a non-nil slice to prevent JSON null.
func buildMutateResponse(result *policy.SaveResult) policyMutateResponse {
	p := result.Policy
	warnings := result.Warnings
	if warnings == nil {
		warnings = make([]string, 0)
	}
	detail := policyDetail{
		ID:          p.ID,
		Name:        p.Name,
		TriggerType: string(p.TriggerType),
		Folder:      extractFolder(p.YAML),
		YAML:        p.YAML,
		CreatedAt:   p.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:   p.UpdatedAt.Format(time.RFC3339Nano),
	}
	if p.PausedAt != nil {
		s := p.PausedAt.Format(time.RFC3339Nano)
		detail.PausedAt = &s
	}
	return policyMutateResponse{
		policyDetail: detail,
		Warnings:     warnings,
	}
}

// Create handles POST /api/v1/policies.
func (h *PolicyHandler) Create(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to read request body", err.Error())
		return
	}

	result, err := h.svc.Create(r.Context(), string(body))
	if err != nil {
		var pe *policy.ParseError
		if errors.As(err, &pe) {
			WriteError(w, http.StatusBadRequest, "invalid policy YAML", pe.Error())
			return
		}
		var ve *policy.ValidationError
		if errors.As(err, &ve) {
			WriteError(w, http.StatusBadRequest, "policy validation failed", strings.Join(ve.Errors, "; "))
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to create policy", err.Error())
		return
	}

	WriteCreated(w, "/api/v1/policies/"+result.Policy.ID, buildMutateResponse(result))
}

// Update handles PUT /api/v1/policies/{id}.
func (h *PolicyHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to read request body", err.Error())
		return
	}

	result, err := h.svc.Update(r.Context(), id, string(body))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		var pe *policy.ParseError
		if errors.As(err, &pe) {
			WriteError(w, http.StatusBadRequest, "invalid policy YAML", pe.Error())
			return
		}
		var ve *policy.ValidationError
		if errors.As(err, &ve) {
			WriteError(w, http.StatusBadRequest, "policy validation failed", strings.Join(ve.Errors, "; "))
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to update policy", err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, buildMutateResponse(result))
}

// Delete handles DELETE /api/v1/policies/{id}.
func (h *PolicyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if _, err := h.store.GetPolicy(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to get policy", err.Error())
		return
	}

	runs, err := h.store.ListActiveRunsByPolicy(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to check active runs", err.Error())
		return
	}
	if len(runs) > 0 {
		WriteError(w, http.StatusConflict, "policy has active runs",
			fmt.Sprintf("%d active run(s) must complete or be cancelled before deletion", len(runs)))
		return
	}

	// ON DELETE CASCADE handles runs, run_steps, and approval_requests automatically.
	if err := h.store.DeletePolicy(r.Context(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete policy", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

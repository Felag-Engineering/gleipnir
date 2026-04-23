package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/httputil"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// PolicyHandler serves policy CRUD endpoints under /api/v1/policies.
type PolicyHandler struct {
	store     *db.Store
	svc       *policy.Service
	poller    PolicyNotifier // may be nil; notified on poll-trigger mutations
	scheduler PolicyNotifier // may be nil; notified on scheduled-trigger mutations
	cron      PolicyNotifier // may be nil; notified on cron-trigger mutations
}

// NewPolicyHandler creates a PolicyHandler backed by the given store and service.
// poller, scheduler, and cron may be nil — notifyTriggers skips nil notifiers.
func NewPolicyHandler(store *db.Store, svc *policy.Service, poller, scheduler, cron PolicyNotifier) *PolicyHandler {
	return &PolicyHandler{store: store, svc: svc, poller: poller, scheduler: scheduler, cron: cron}
}

// notifyTriggers dispatches to the appropriate background component after a
// successful policy mutation. It is best-effort and synchronous — errors inside
// Notify are handled by the notifier itself (logged at warn). Callers must pass
// the request context so the DB read inside Notify is bounded by the request.
func (h *PolicyHandler) notifyTriggers(ctx context.Context, policyID string, triggerType model.TriggerType) {
	switch triggerType {
	case model.TriggerTypePoll:
		if h.poller != nil {
			h.poller.Notify(ctx, policyID)
		}
	case model.TriggerTypeScheduled:
		if h.scheduler != nil {
			h.scheduler.Notify(ctx, policyID)
		}
	case model.TriggerTypeCron:
		if h.cron != nil {
			h.cron.Notify(ctx, policyID)
		}
	}
}

type runSummary struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	TokenCost int64  `json:"token_cost"`
}

type policyListItem struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	TriggerType  string      `json:"trigger_type"`
	Folder       string      `json:"folder"`
	Model        string      `json:"model"`
	ToolCount    int         `json:"tool_count"`
	ToolRefs     []string    `json:"tool_refs"`
	AvgTokenCost int64       `json:"avg_token_cost"`
	RunCount     int64       `json:"run_count"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
	PausedAt     *string     `json:"paused_at"`
	LatestRun    *runSummary `json:"latest_run"`
	NextFireAt   *string     `json:"next_fire_at"`
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
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list policies", err.Error())
		return
	}

	items := make([]policyListItem, 0, len(rows))
	for _, row := range rows {
		summary := parsePolicySummary(row.Yaml)
		toolRefs := summary.toolRefs()
		item := policyListItem{
			ID:           row.ID,
			Name:         row.Name,
			TriggerType:  row.TriggerType,
			Folder:       summary.Folder,
			Model:        summary.Model,
			ToolCount:    len(summary.Capabilities.Tools),
			ToolRefs:     toolRefs,
			AvgTokenCost: row.AvgTokenCost,
			RunCount:     row.RunCount,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
			PausedAt:     row.PausedAt,
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

		item.NextFireAt = computeNextFireAt(row.TriggerType, summary, item.LatestRun)

		items = append(items, item)
	}

	httputil.WriteJSON(w, http.StatusOK, items)
}

// toPolicyDetail converts a db.Policy row into a policyDetail response struct.
func toPolicyDetail(p db.Policy) policyDetail {
	return policyDetail{
		ID:          p.ID,
		Name:        p.Name,
		TriggerType: p.TriggerType,
		Folder:      extractFolder(p.Yaml),
		YAML:        p.Yaml,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		PausedAt:    p.PausedAt,
	}
}

// Get handles GET /api/v1/policies/{id}.
func (h *PolicyHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	policy, err := h.store.GetPolicy(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get policy", err.Error())
		return
	}

	httputil.WriteJSON(w, http.StatusOK, toPolicyDetail(policy))
}

// policyYAMLSummary holds the fields extracted from a raw policy YAML blob
// for list responses. Parsing once and reading multiple fields avoids repeated
// unmarshal calls per row in the List handler.
type policyToolEntry struct {
	Tool string `yaml:"tool"`
}

type policyYAMLSummary struct {
	Folder       string `yaml:"folder"`
	Model        string `yaml:"model"`
	Capabilities struct {
		Tools []policyToolEntry `yaml:"tools"`
	} `yaml:"capabilities"`
	Trigger struct {
		FireAt   []string `yaml:"fire_at"`  // scheduled only, RFC3339 timestamps
		Interval string   `yaml:"interval"` // poll only, Go duration string e.g. "5m"
	} `yaml:"trigger"`
}

// toolRefs extracts the tool reference strings (e.g. "server.tool_name")
// from the parsed capabilities.
func (s policyYAMLSummary) toolRefs() []string {
	refs := make([]string, 0, len(s.Capabilities.Tools))
	for _, t := range s.Capabilities.Tools {
		if t.Tool != "" {
			refs = append(refs, t.Tool)
		}
	}
	return refs
}

// parsePolicySummary unmarshals rawYAML into a policyYAMLSummary.
// Parse errors are logged at warn and return zero values — a corrupt YAML blob
// in the DB should not crash the list endpoint.
func parsePolicySummary(rawYAML string) policyYAMLSummary {
	var v policyYAMLSummary
	if err := yaml.Unmarshal([]byte(rawYAML), &v); err != nil {
		slog.Warn("parsePolicySummary: failed to parse policy YAML", "err", err)
	}
	return v
}

// computeNextFireAt returns the next scheduled fire time for scheduled and poll
// trigger types, or nil for all others. Parse errors are silently ignored so a
// bad YAML value never breaks the list endpoint.
func computeNextFireAt(triggerType string, summary policyYAMLSummary, latestRun *runSummary) *string {
	now := time.Now().UTC()

	switch triggerType {
	case string(model.TriggerTypeScheduled):
		var earliest *time.Time
		for _, raw := range summary.Trigger.FireAt {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				continue
			}
			if t.After(now) && (earliest == nil || t.Before(*earliest)) {
				tc := t
				earliest = &tc
			}
		}
		if earliest != nil {
			s := earliest.UTC().Format(time.RFC3339)
			return &s
		}

	case string(model.TriggerTypePoll):
		if latestRun == nil {
			return nil
		}
		interval, err := time.ParseDuration(summary.Trigger.Interval)
		if err != nil || interval <= 0 {
			return nil
		}
		startedAt, err := time.Parse(time.RFC3339Nano, latestRun.StartedAt)
		if err != nil {
			// Also try without nanoseconds
			startedAt, err = time.Parse(time.RFC3339, latestRun.StartedAt)
			if err != nil {
				return nil
			}
		}
		next := startedAt.Add(interval).UTC()
		s := next.Format(time.RFC3339)
		return &s
	}

	return nil
}

// extractFolder parses the folder field from a raw policy YAML blob.
// Folder is cosmetic/UI-only (ADR-020); returns "" on parse failure.
// Used by Get and buildMutateResponse where only the folder field is needed.
func extractFolder(rawYAML string) string {
	return parsePolicySummary(rawYAML).Folder
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
	// Convert model.Policy time fields to the string format db.Policy uses so
	// toPolicyDetail can handle both code paths uniformly.
	var pausedAt *string
	if p.PausedAt != nil {
		s := p.PausedAt.Format(time.RFC3339Nano)
		pausedAt = &s
	}
	dbPolicy := db.Policy{
		ID:          p.ID,
		Name:        p.Name,
		TriggerType: string(p.TriggerType),
		Yaml:        p.YAML,
		CreatedAt:   p.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:   p.UpdatedAt.Format(time.RFC3339Nano),
		PausedAt:    pausedAt,
	}
	return policyMutateResponse{
		policyDetail: toPolicyDetail(dbPolicy),
		Warnings:     warnings,
	}
}

// Create handles POST /api/v1/policies.
func (h *PolicyHandler) Create(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to read request body", err.Error())
		return
	}

	result, err := h.svc.Create(r.Context(), string(body))
	if err != nil {
		var pe *policy.ParseError
		if errors.As(err, &pe) {
			httputil.WriteError(w, http.StatusBadRequest, "invalid policy YAML", pe.Error())
			return
		}
		var ve *policy.ValidationError
		if errors.As(err, &ve) {
			messages := make([]string, 0, len(ve.Errors))
			issues := make([]httputil.ErrorIssue, 0, len(ve.Errors))
			for _, iss := range ve.Errors {
				messages = append(messages, iss.Message)
				issues = append(issues, httputil.ErrorIssue{Field: iss.Field, Message: iss.Message})
			}
			httputil.WriteValidationError(w, http.StatusBadRequest,
				"policy validation failed", strings.Join(messages, "; "), issues)
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to create policy", err.Error())
		return
	}

	h.notifyTriggers(r.Context(), result.Policy.ID, result.Policy.TriggerType)
	httputil.WriteCreated(w, "/api/v1/policies/"+result.Policy.ID, buildMutateResponse(result))
}

// Update handles PUT /api/v1/policies/{id}.
func (h *PolicyHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to read request body", err.Error())
		return
	}

	result, err := h.svc.Update(r.Context(), id, string(body))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		var pe *policy.ParseError
		if errors.As(err, &pe) {
			httputil.WriteError(w, http.StatusBadRequest, "invalid policy YAML", pe.Error())
			return
		}
		var ve *policy.ValidationError
		if errors.As(err, &ve) {
			messages := make([]string, 0, len(ve.Errors))
			issues := make([]httputil.ErrorIssue, 0, len(ve.Errors))
			for _, iss := range ve.Errors {
				messages = append(messages, iss.Message)
				issues = append(issues, httputil.ErrorIssue{Field: iss.Field, Message: iss.Message})
			}
			httputil.WriteValidationError(w, http.StatusBadRequest,
				"policy validation failed", strings.Join(messages, "; "), issues)
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update policy", err.Error())
		return
	}

	h.notifyTriggers(r.Context(), result.Policy.ID, result.Policy.TriggerType)
	httputil.WriteJSON(w, http.StatusOK, buildMutateResponse(result))
}

// Pause handles POST /api/v1/policies/{id}/pause.
// It sets paused_at to the current time, preventing webhook and manual triggers from firing.
// Returns 409 if the policy is already paused.
func (h *PolicyHandler) Pause(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	dbPolicy, err := h.store.GetPolicy(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get policy", err.Error())
		return
	}

	if dbPolicy.PausedAt != nil {
		httputil.WriteError(w, http.StatusConflict, "policy is already paused", "")
		return
	}

	if err := h.svc.SetPolicyPausedAt(r.Context(), id); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to pause policy", err.Error())
		return
	}

	updated, err := h.store.GetPolicy(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to fetch updated policy", err.Error())
		return
	}

	h.notifyTriggers(r.Context(), id, model.TriggerType(updated.TriggerType))
	httputil.WriteJSON(w, http.StatusOK, toPolicyDetail(updated))
}

// Resume handles POST /api/v1/policies/{id}/resume.
// It clears paused_at, allowing webhook and manual triggers to fire again.
// Returns 409 if the policy is not currently paused.
func (h *PolicyHandler) Resume(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	dbPolicy, err := h.store.GetPolicy(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get policy", err.Error())
		return
	}

	if dbPolicy.PausedAt == nil {
		httputil.WriteError(w, http.StatusConflict, "policy is not paused", "")
		return
	}

	if err := h.svc.ClearPolicyPausedAt(r.Context(), id); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to resume policy", err.Error())
		return
	}

	updated, err := h.store.GetPolicy(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to fetch updated policy", err.Error())
		return
	}

	h.notifyTriggers(r.Context(), id, model.TriggerType(updated.TriggerType))
	httputil.WriteJSON(w, http.StatusOK, toPolicyDetail(updated))
}

// Delete handles DELETE /api/v1/policies/{id}.
func (h *PolicyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Capture trigger type before deleting — we need it to notify the right
	// background component after the row is gone (Notify will observe ErrNoRows
	// and cancel any running loop/timers cleanly).
	existing, err := h.store.GetPolicy(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "policy not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get policy", err.Error())
		return
	}

	runs, err := h.store.ListActiveRunsByPolicy(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to check active runs", err.Error())
		return
	}
	if len(runs) > 0 {
		httputil.WriteError(w, http.StatusConflict, "policy has active runs",
			fmt.Sprintf("%d active run(s) must complete or be cancelled before deletion", len(runs)))
		return
	}

	// ON DELETE CASCADE handles runs, run_steps, and approval_requests automatically.
	if err := h.store.DeletePolicy(r.Context(), id); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to delete policy", err.Error())
		return
	}

	// Notify after delete so the background component cancels any running loop
	// or pending timers. Notify reads ErrNoRows from the DB and cancels cleanly.
	h.notifyTriggers(r.Context(), id, model.TriggerType(existing.TriggerType))

	w.WriteHeader(http.StatusNoContent)
}

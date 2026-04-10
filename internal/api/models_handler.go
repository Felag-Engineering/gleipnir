package api

import (
	"context"
	"net/http"

	"github.com/rapp992/gleipnir/internal/llm"
)

// ModelLister is an alias for llm.ModelLister, kept here so existing callers
// that reference api.ModelLister continue to compile without changes.
type ModelLister = llm.ModelLister

// EnabledModel identifies a model that has been explicitly enabled by an admin.
type EnabledModel struct {
	Provider  string
	ModelName string
}

// ModelFilter provides the set of enabled models so the public /models
// endpoint can restrict to only those models.
type ModelFilter interface {
	ListEnabledModels(ctx context.Context) ([]EnabledModel, error)
}

// ModelsHandler serves the /api/v1/models endpoints.
type ModelsHandler struct {
	lister ModelLister
	filter ModelFilter
}

// NewModelsHandler creates a ModelsHandler backed by the given lister.
// filter may be nil — no models will be filtered.
func NewModelsHandler(lister ModelLister, filter ModelFilter) *ModelsHandler {
	return &ModelsHandler{lister: lister, filter: filter}
}

type modelResponse struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

type modelsListResponse struct {
	Provider string          `json:"provider"`
	Models   []modelResponse `json:"models"`
}

// List handles GET /api/v1/models.
// Optional query param: ?provider=google — filters to a single provider.
func (h *ModelsHandler) List(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")

	enabled := h.loadEnabledSet(r.Context())

	if provider != "" {
		models, err := h.lister.ListModels(r.Context(), provider)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "failed to list models", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, []modelsListResponse{
			{Provider: provider, Models: filterModels(toModelResponses(models), provider, enabled)},
		})
		return
	}

	all, err := h.lister.ListAllModels(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list models", err.Error())
		return
	}

	result := make([]modelsListResponse, 0, len(all))
	for prov, models := range all {
		result = append(result, modelsListResponse{
			Provider: prov,
			Models:   filterModels(toModelResponses(models), prov, enabled),
		})
	}
	WriteJSON(w, http.StatusOK, result)
}

// Refresh handles POST /api/v1/models/refresh.
// Optional query param: ?provider=google — refreshes only that provider.
// Returns the fresh model list after invalidation.
func (h *ModelsHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")

	if provider != "" {
		if err := h.lister.InvalidateModelCache(provider); err != nil {
			WriteError(w, http.StatusBadRequest, "failed to invalidate cache", err.Error())
			return
		}
		models, err := h.lister.ListModels(r.Context(), provider)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to list models after refresh", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, []modelsListResponse{
			{Provider: provider, Models: toModelResponses(models)},
		})
		return
	}

	h.lister.InvalidateAllModelCaches()

	all, err := h.lister.ListAllModels(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list models after refresh", err.Error())
		return
	}

	result := make([]modelsListResponse, 0, len(all))
	for prov, models := range all {
		result = append(result, modelsListResponse{
			Provider: prov,
			Models:   toModelResponses(models),
		})
	}
	WriteJSON(w, http.StatusOK, result)
}

func toModelResponses(models []llm.ModelInfo) []modelResponse {
	resp := make([]modelResponse, len(models))
	for i, m := range models {
		resp[i] = modelResponse{Name: m.Name, DisplayName: m.DisplayName}
	}
	return resp
}

// loadEnabledSet builds a set of "provider:model" keys from the filter.
// Returns nil if no filter is configured or the query fails (nil means no
// restriction — all models pass through).
func (h *ModelsHandler) loadEnabledSet(ctx context.Context) map[string]struct{} {
	if h.filter == nil {
		return nil
	}
	rows, err := h.filter.ListEnabledModels(ctx)
	if err != nil {
		return nil
	}
	set := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		set[r.Provider+":"+r.ModelName] = struct{}{}
	}
	return set
}

// filterModels keeps only models whose "provider:name" key appears in the
// enabled set. When enabled is nil (no filter configured), all models are
// returned unchanged. When enabled is non-nil but empty, no models are returned.
func filterModels(models []modelResponse, provider string, enabled map[string]struct{}) []modelResponse {
	if enabled == nil {
		return models
	}
	filtered := make([]modelResponse, 0, len(models))
	for _, m := range models {
		if _, ok := enabled[provider+":"+m.Name]; ok {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

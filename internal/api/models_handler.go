package api

import (
	"context"
	"net/http"

	"github.com/rapp992/gleipnir/internal/llm"
)

// ModelLister is the subset of llm.ProviderRegistry used by ModelsHandler.
// Defined as an interface so the api package does not depend on the concrete registry.
type ModelLister interface {
	ListModels(ctx context.Context, provider string) ([]llm.ModelInfo, error)
	ListAllModels(ctx context.Context) (map[string][]llm.ModelInfo, error)
	InvalidateModelCache(provider string) error
	InvalidateAllModelCaches()
}

// DisabledModel identifies a model that has been disabled by an admin.
type DisabledModel struct {
	Provider  string
	ModelName string
}

// ModelFilter provides the set of disabled models so the public /models
// endpoint can exclude them.
type ModelFilter interface {
	ListDisabledModels(ctx context.Context) ([]DisabledModel, error)
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

	disabled := h.loadDisabledSet(r.Context())

	if provider != "" {
		models, err := h.lister.ListModels(r.Context(), provider)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "failed to list models", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, []modelsListResponse{
			{Provider: provider, Models: filterModels(toModelResponses(models), provider, disabled)},
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
			Models:   filterModels(toModelResponses(models), prov, disabled),
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

// loadDisabledSet builds a set of "provider:model" keys from the filter.
// Returns nil if no filter is configured or the query fails.
func (h *ModelsHandler) loadDisabledSet(ctx context.Context) map[string]struct{} {
	if h.filter == nil {
		return nil
	}
	rows, err := h.filter.ListDisabledModels(ctx)
	if err != nil {
		return nil
	}
	set := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		set[r.Provider+":"+r.ModelName] = struct{}{}
	}
	return set
}

// filterModels removes disabled models from the response slice.
func filterModels(models []modelResponse, provider string, disabled map[string]struct{}) []modelResponse {
	if len(disabled) == 0 {
		return models
	}
	filtered := make([]modelResponse, 0, len(models))
	for _, m := range models {
		if _, ok := disabled[provider+":"+m.Name]; !ok {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

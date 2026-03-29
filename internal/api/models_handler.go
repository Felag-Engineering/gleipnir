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

// ModelsHandler serves the /api/v1/models endpoints.
type ModelsHandler struct {
	lister ModelLister
}

// NewModelsHandler creates a ModelsHandler backed by the given lister.
func NewModelsHandler(lister ModelLister) *ModelsHandler {
	return &ModelsHandler{lister: lister}
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

	if provider != "" {
		models, err := h.lister.ListModels(r.Context(), provider)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "failed to list models", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, []modelsListResponse{
			{Provider: provider, Models: toModelResponses(models)},
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
			Models:   toModelResponses(models),
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

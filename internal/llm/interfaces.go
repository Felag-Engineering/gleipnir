package llm

import "context"

// ModelLister is the subset of ProviderRegistry used by model listing endpoints.
// Defined as an interface so handler packages do not depend on the concrete registry.
type ModelLister interface {
	ListModels(ctx context.Context, provider string) ([]ModelInfo, error)
	ListAllModels(ctx context.Context) (map[string][]ModelInfo, error)
	InvalidateModelCache(provider string) error
	InvalidateAllModelCaches()
}

package llm

import (
	"context"
	"fmt"
	"sync"
)

// ProviderRegistry maps provider name strings to LLMClient implementations.
// Register at startup, then call Get concurrently from trigger handlers.
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]LLMClient
}

// NewProviderRegistry returns an empty registry ready for use.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]LLMClient),
	}
}

// Register adds client under name, overwriting any previous entry.
func (r *ProviderRegistry) Register(name string, client LLMClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = client
}

// Unregister removes the client registered under name. No-op if not present.
func (r *ProviderRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, name)
}

// Providers returns the names of all registered providers.
func (r *ProviderRegistry) Providers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// Get returns the client registered under name.
// Returns an error containing the provider name if no client is registered.
func (r *ProviderRegistry) Get(name string) (LLMClient, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	client, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown LLM provider %q", name)
	}
	return client, nil
}

// ValidateProviderOptions looks up provider by name and delegates option
// validation to its client. This method satisfies the policy.OptionsValidator
// interface so *ProviderRegistry can be passed to policy.NewService without
// the policy package importing internal/llm.
func (r *ProviderRegistry) ValidateProviderOptions(provider string, options map[string]any) error {
	client, err := r.Get(provider)
	if err != nil {
		return fmt.Errorf("unknown provider %q: cannot validate model options", provider)
	}
	if err := client.ValidateOptions(options); err != nil {
		return fmt.Errorf("provider %q: %w", provider, err)
	}
	return nil
}

// ValidateModelName looks up provider by name and delegates model name
// validation to its client. This method satisfies the policy.ModelValidator
// interface so *ProviderRegistry can be passed to policy.NewService without
// the policy package importing internal/llm.
func (r *ProviderRegistry) ValidateModelName(ctx context.Context, provider, modelName string) error {
	client, err := r.Get(provider)
	if err != nil {
		return fmt.Errorf("unknown provider %q: cannot validate model name", provider)
	}
	if err := client.ValidateModelName(ctx, modelName); err != nil {
		return fmt.Errorf("provider %q: %w", provider, err)
	}
	return nil
}

// ListModels returns models from the named provider.
func (r *ProviderRegistry) ListModels(ctx context.Context, provider string) ([]ModelInfo, error) {
	client, err := r.Get(provider)
	if err != nil {
		return nil, fmt.Errorf("unknown provider %q: cannot list models", provider)
	}
	return client.ListModels(ctx)
}

// ListAllModels returns models from every registered provider, keyed by provider name.
func (r *ProviderRegistry) ListAllModels(ctx context.Context) (map[string][]ModelInfo, error) {
	r.mu.RLock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	r.mu.RUnlock()

	result := make(map[string][]ModelInfo, len(names))
	for _, name := range names {
		client, err := r.Get(name)
		if err != nil {
			continue // provider was unregistered between snapshot and iteration
		}
		models, err := client.ListModels(ctx)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		result[name] = models
	}
	return result, nil
}

// InvalidateModelCache clears the cached model list for the named provider.
func (r *ProviderRegistry) InvalidateModelCache(provider string) error {
	client, err := r.Get(provider)
	if err != nil {
		return fmt.Errorf("unknown provider %q: cannot invalidate cache", provider)
	}
	client.InvalidateModelCache()
	return nil
}

// InvalidateAllModelCaches clears model caches for all registered providers.
func (r *ProviderRegistry) InvalidateAllModelCaches() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, client := range r.providers {
		client.InvalidateModelCache()
	}
}

package llm

import (
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

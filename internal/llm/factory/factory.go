// Package factory provides NewClientForProvider, which maps a provider name
// string to a concrete LLMClient. It lives in its own sub-package to avoid
// an import cycle: the provider packages (anthropic, google, openai) all
// import internal/llm for the shared interface types, so the factory cannot
// live in internal/llm itself.
package factory

import (
	"context"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/llm/anthropic"
	"github.com/felag-engineering/gleipnir/internal/llm/google"
	"github.com/felag-engineering/gleipnir/internal/llm/openai"
)

// NewClientForProvider creates an LLMClient for the named provider using the
// given API key. It returns an error for unknown provider names, so callers
// have a single, obvious touch point when adding new providers.
func NewClientForProvider(ctx context.Context, provider, apiKey string) (llm.LLMClient, error) {
	switch provider {
	case "anthropic":
		return anthropic.NewClient(apiKey), nil
	case "google":
		client, err := google.NewClient(ctx, apiKey)
		if err != nil {
			return nil, fmt.Errorf("create google client: %w", err)
		}
		return client, nil
	case "openai":
		return openai.NewClient(apiKey), nil
	default:
		return nil, fmt.Errorf("unknown provider %q", provider)
	}
}

package api_test

import (
	"context"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/llm/anthropic"
	"github.com/felag-engineering/gleipnir/internal/llm/google"
	"github.com/felag-engineering/gleipnir/internal/llm/openai"
)

// TestAllCuratedModelsHaveDisplayNames ensures that every model returned by
// each provider's ListModels has a corresponding entry in ModelDisplayNames.
// This test fails immediately when a new model is added to any curated list
// without a matching display name — preventing silent $0.00 cost chart entries.
func TestAllCuratedModelsHaveDisplayNames(t *testing.T) {
	ctx := context.Background()

	// Zero-value clients are safe: ListModels reads the curated slice without
	// any network call, so no API key or HTTP server is needed.
	clients := []struct {
		provider string
		list     func() ([]string, error)
	}{
		{
			provider: "anthropic",
			list: func() ([]string, error) {
				c := &anthropic.AnthropicClient{}
				models, err := c.ListModels(ctx)
				if err != nil {
					return nil, err
				}
				names := make([]string, len(models))
				for i, m := range models {
					names[i] = m.Name
				}
				return names, nil
			},
		},
		{
			provider: "google",
			list: func() ([]string, error) {
				c := &google.GeminiClient{}
				models, err := c.ListModels(ctx)
				if err != nil {
					return nil, err
				}
				names := make([]string, len(models))
				for i, m := range models {
					names[i] = m.Name
				}
				return names, nil
			},
		},
		{
			provider: "openai",
			list: func() ([]string, error) {
				c := &openai.Client{}
				models, err := c.ListModels(ctx)
				if err != nil {
					return nil, err
				}
				names := make([]string, len(models))
				for i, m := range models {
					names[i] = m.Name
				}
				return names, nil
			},
		},
	}

	for _, tc := range clients {
		t.Run(tc.provider, func(t *testing.T) {
			modelNames, err := tc.list()
			if err != nil {
				t.Fatalf("ListModels for %s: %v", tc.provider, err)
			}
			for _, name := range modelNames {
				if _, ok := api.ModelDisplayNames[name]; !ok {
					t.Errorf(
						"model %q from %s curated list has no entry in api.ModelDisplayNames — add it to internal/api/modelnames.go",
						name, tc.provider,
					)
				}
			}
		})
	}
}

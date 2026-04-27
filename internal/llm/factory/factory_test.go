package factory_test

import (
	"context"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/llm/factory"
)

func TestNewClientForProvider(t *testing.T) {
	ctx := context.Background()

	knownProviders := []string{"anthropic", "google", "openai"}
	for _, provider := range knownProviders {
		t.Run(provider, func(t *testing.T) {
			client, err := factory.NewClientForProvider(ctx, provider, "test-key")
			if err != nil {
				t.Fatalf("expected no error for provider %q, got: %v", provider, err)
			}
			if client == nil {
				t.Fatalf("expected non-nil client for provider %q", provider)
			}
		})
	}

	t.Run("unknown provider", func(t *testing.T) {
		client, err := factory.NewClientForProvider(ctx, "nonexistent", "test-key")
		if err == nil {
			t.Fatal("expected error for unknown provider, got nil")
		}
		if client != nil {
			t.Fatal("expected nil client for unknown provider")
		}
		if !strings.Contains(err.Error(), "nonexistent") {
			t.Errorf("error message should contain the unknown provider name; got: %q", err.Error())
		}
	})
}

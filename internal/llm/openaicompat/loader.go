package openaicompat

import (
	"context"
	"log/slog"

	"github.com/rapp992/gleipnir/internal/llm"
)

// LoaderRow is the subset of openai_compat_providers needed at load time.
// It decouples the loader from the exact sqlc-generated struct name so
// main.go can adapt the sqlc rows into this shape at the call site.
type LoaderRow struct {
	Name            string
	BaseURL         string
	APIKeyEncrypted string
}

// LoaderQuerier is the interface main.go implements to hand rows to the
// loader. In production this wraps `db.Queries.ListOpenAICompatProviders`.
type LoaderQuerier interface {
	ListOpenAICompatProvidersForLoader(ctx context.Context) ([]LoaderRow, error)
}

// DecryptFunc decrypts a ciphertext using the provided key. In production
// main.go passes admin.Decrypt; in tests the loader_test file can pass its
// own helper. Accepting it as a parameter avoids an import cycle between
// internal/llm/openai and internal/admin.
type DecryptFunc func(key []byte, encoded string) (string, error)

// LoadAndRegister reads all rows from the querier, decrypts each API key
// with decrypt, constructs a *Client for each, and registers it in the
// registry under the row's name. Rows whose ciphertext cannot be decrypted
// are logged and skipped; LoadAndRegister returns an error only if the
// initial list query fails.
func LoadAndRegister(ctx context.Context, q LoaderQuerier, encKey []byte, registry *llm.ProviderRegistry, decrypt DecryptFunc) error {
	rows, err := q.ListOpenAICompatProvidersForLoader(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		plaintext, err := decrypt(encKey, row.APIKeyEncrypted)
		if err != nil {
			slog.Error("openai loader: decrypt failed, skipping row",
				"name", row.Name, "err", err)
			continue
		}
		client := NewClient(row.BaseURL, plaintext, WithProviderName(row.Name))
		registry.Register(row.Name, client)
		slog.Info("openai loader: registered provider",
			"name", row.Name, "base_url", row.BaseURL)
	}
	return nil
}

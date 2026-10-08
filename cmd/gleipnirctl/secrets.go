package main

import (
	"context"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// secretRow is one stored ciphertext plus enough context to report it and to
// write a replacement back. ID identifies the row (a key, name, or UUID) and is
// safe to print; it never contains secret material.
type secretRow struct {
	ID         string
	Ciphertext string
	// rewrite stores newCiphertext in place of Ciphertext for this row.
	rewrite func(ctx context.Context, q *db.Queries, newCiphertext, now string) error
}

// secretColumn describes one encrypted database column.
type secretColumn struct {
	Table  string
	Column string
	// list returns every non-NULL ciphertext in the column.
	list func(ctx context.Context, q *db.Queries) ([]secretRow, error)
}

func (c secretColumn) name() string { return c.Table + "." + c.Column }

// Names of the encrypted columns, used to look up per-column counts.
const (
	colProviderKeys     = "system_settings.value"
	colOpenAICompatKeys = "openai_compat_providers.api_key_encrypted"
	colWebhookSecrets   = "policies.webhook_secret_encrypted"
	colMCPAuthHeaders   = "mcp_servers.auth_headers_encrypted"
	colPluginCreds      = "plugin_instances.credentials_encrypted"
)

// encryptedColumns is the single enumeration of every at-rest secret under
// GLEIPNIR_ENCRYPTION_KEY. rotate-key, verify-keys and check all walk this
// list, so a newly encrypted column added here is covered by all of them.
// Add new encrypted columns here and nowhere else.
func encryptedColumns() []secretColumn {
	return []secretColumn{
		{
			Table: "system_settings", Column: "value",
			list: func(ctx context.Context, q *db.Queries) ([]secretRow, error) {
				// Only rows whose key ends in "_api_key" hold ciphertext.
				rows, err := q.ListAPIKeySystemSettings(ctx)
				if err != nil {
					return nil, fmt.Errorf("list api key settings: %w", err)
				}
				out := make([]secretRow, 0, len(rows))
				for _, r := range rows {
					key := r.Key
					out = append(out, secretRow{
						ID:         "key=" + key,
						Ciphertext: r.Value,
						rewrite: func(ctx context.Context, q *db.Queries, newCiphertext, now string) error {
							return q.UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{
								Key: key, Value: newCiphertext, UpdatedAt: now,
							})
						},
					})
				}
				return out, nil
			},
		},
		{
			Table: "openai_compat_providers", Column: "api_key_encrypted",
			list: func(ctx context.Context, q *db.Queries) ([]secretRow, error) {
				rows, err := q.ListOpenAICompatProviders(ctx)
				if err != nil {
					return nil, fmt.Errorf("list openai-compat providers: %w", err)
				}
				out := make([]secretRow, 0, len(rows))
				for _, r := range rows {
					id := r.ID
					out = append(out, secretRow{
						ID:         fmt.Sprintf("id=%d name=%q", r.ID, r.Name),
						Ciphertext: r.ApiKeyEncrypted,
						rewrite: func(ctx context.Context, q *db.Queries, newCiphertext, now string) error {
							return q.UpdateOpenAICompatProviderAPIKey(ctx, db.UpdateOpenAICompatProviderAPIKeyParams{
								ApiKeyEncrypted: newCiphertext, UpdatedAt: now, ID: id,
							})
						},
					})
				}
				return out, nil
			},
		},
		{
			Table: "policies", Column: "webhook_secret_encrypted",
			list: func(ctx context.Context, q *db.Queries) ([]secretRow, error) {
				rows, err := q.ListPolicyWebhookSecrets(ctx)
				if err != nil {
					return nil, fmt.Errorf("list policy webhook secrets: %w", err)
				}
				out := make([]secretRow, 0, len(rows))
				for _, r := range rows {
					if r.WebhookSecretEncrypted == nil {
						continue
					}
					id := r.ID
					out = append(out, secretRow{
						ID:         "id=" + id,
						Ciphertext: *r.WebhookSecretEncrypted,
						rewrite: func(ctx context.Context, q *db.Queries, newCiphertext, now string) error {
							return q.SetPolicyWebhookSecret(ctx, db.SetPolicyWebhookSecretParams{
								Ciphertext: &newCiphertext, UpdatedAt: now, ID: id,
							})
						},
					})
				}
				return out, nil
			},
		},
		{
			Table: "mcp_servers", Column: "auth_headers_encrypted",
			list: func(ctx context.Context, q *db.Queries) ([]secretRow, error) {
				rows, err := q.ListMCPServersWithAuthHeaders(ctx)
				if err != nil {
					return nil, fmt.Errorf("list mcp server auth headers: %w", err)
				}
				out := make([]secretRow, 0, len(rows))
				for _, r := range rows {
					if r.AuthHeadersEncrypted == nil {
						continue
					}
					id := r.ID
					out = append(out, secretRow{
						ID:         "id=" + id,
						Ciphertext: *r.AuthHeadersEncrypted,
						rewrite: func(ctx context.Context, q *db.Queries, newCiphertext, _ string) error {
							return q.UpdateMCPServerAuthHeaders(ctx, db.UpdateMCPServerAuthHeadersParams{
								AuthHeadersEncrypted: &newCiphertext, ID: id,
							})
						},
					})
				}
				return out, nil
			},
		},
		{
			// Holds plugin credentials of every strategy, including OAuth tokens.
			// Plugin config secrets (x-gleipnir-secret) live in config_json and
			// are not encrypted at rest, so they are not listed here.
			Table: "plugin_instances", Column: "credentials_encrypted",
			list: func(ctx context.Context, q *db.Queries) ([]secretRow, error) {
				rows, err := q.ListPluginInstances(ctx)
				if err != nil {
					return nil, fmt.Errorf("list plugin instances: %w", err)
				}
				out := make([]secretRow, 0, len(rows))
				for _, r := range rows {
					if r.CredentialsEncrypted == nil {
						continue
					}
					inst := r
					out = append(out, secretRow{
						ID:         "id=" + inst.ID,
						Ciphertext: *inst.CredentialsEncrypted,
						rewrite: func(ctx context.Context, q *db.Queries, newCiphertext, now string) error {
							n, err := q.UpdatePluginInstanceCredentials(ctx, db.UpdatePluginInstanceCredentialsParams{
								CredentialsEncrypted: &newCiphertext,
								CredentialsExpiresAt: inst.CredentialsExpiresAt,
								UpdatedAt:            now,
								ID:                   inst.ID,
								ExpectedVersion:      inst.Version,
							})
							if err != nil {
								return err
							}
							if n != 1 {
								return fmt.Errorf("plugin instance changed during rotation (version conflict)")
							}
							return nil
						},
					})
				}
				return out, nil
			},
		},
	}
}

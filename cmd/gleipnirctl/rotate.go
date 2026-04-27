// Package main implements the gleipnirctl local admin CLI.
//
// rotate.go contains the core rotation logic: Rotate re-encrypts every
// at-rest secret in the database (provider API keys in system_settings,
// OpenAI-compat provider keys, and per-policy webhook secrets) under a new
// AES-256-GCM key in a single transaction. The Cobra command wiring lives in
// rotatekey.go. Run this operation with the server stopped; Rotate refuses to
// proceed if another process holds the database write lock.
package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
)

// Rotate re-encrypts all secrets under newKey. oldKey and newKey are already
// parsed and validated by the caller. dbPath is the SQLite file to open.
// Returns a shell exit code (0 success, 1 error, 3 DB busy).
func Rotate(ctx context.Context, dbPath string, oldKey, newKey []byte, dryRun bool, out, errOut io.Writer) int {
	store, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(errOut, "error: open db: %v\n", err)
		return 1
	}
	defer store.Close()

	// Liveness probe: detect whether another process holds the DB write lock
	// BEFORE running migrations. We must probe first because Migrate itself
	// performs writes that will hit SQLITE_BUSY if the server is running —
	// that would produce a confusing "migrate" error instead of the clear
	// "stop the server first" message.
	//
	// We set locking_mode=EXCLUSIVE so our next write attempt tries to acquire
	// an exclusive lock immediately. modernc.org/sqlite surfaces SQLITE_BUSY
	// in the error text when contention is detected (no busy_timeout is set).
	if _, err := store.DB().ExecContext(ctx, "PRAGMA locking_mode=EXCLUSIVE"); err != nil {
		fmt.Fprintf(errOut, "error: set locking mode: %v\n", err)
		return 1
	}

	probeTx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		if isBusy(err) {
			fmt.Fprintln(errOut, "error: refusing to rotate while another process holds the DB; stop the server first")
			return 3
		}
		fmt.Fprintf(errOut, "error: begin probe transaction: %v\n", err)
		return 1
	}

	// Write to the main DB schema (not a TEMP table) so the probe actually
	// acquires the WAL write lock. We create/use a real table in the main file;
	// if another process holds the write lock this INSERT will return SQLITE_BUSY.
	_, probeErr := probeTx.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS _rotate_probe (x INTEGER)`,
	)
	if probeErr == nil {
		_, probeErr = probeTx.ExecContext(ctx, `INSERT INTO _rotate_probe(x) VALUES (0)`)
	}
	// Always roll back — the CREATE TABLE and INSERT are never intended to persist.
	// A crash between the CREATE and this Rollback will leave _rotate_probe in the
	// schema, but it is harmless: it holds no data and IF NOT EXISTS prevents errors
	// on subsequent runs.
	_ = probeTx.Rollback()

	if probeErr != nil {
		if isBusy(probeErr) {
			fmt.Fprintln(errOut, "error: refusing to rotate while another process holds the DB; stop the server first")
			return 3
		}
		fmt.Fprintf(errOut, "error: write probe failed: %v\n", probeErr)
		return 1
	}

	// Migration is idempotent; it ensures we can run rotation against a restored
	// backup that may be on an older schema version.
	if err := store.Migrate(ctx); err != nil {
		fmt.Fprintf(errOut, "error: migrate db: %v\n", err)
		return 1
	}

	providerCount, compatCount, webhookCount, mcpHeaderCount, rotErr := rotateWithDryRun(ctx, store, oldKey, newKey, dryRun)
	if rotErr != nil {
		fmt.Fprintf(errOut, "error: %v\n", rotErr)
		return 1
	}

	summary := fmt.Sprintf("re-encrypted %d provider keys, %d openai-compat keys, %d webhook secrets, %d MCP auth header sets",
		providerCount, compatCount, webhookCount, mcpHeaderCount)
	if dryRun {
		summary += " (dry-run; no changes written)"
	}
	fmt.Fprintln(out, summary)
	return 0
}

// rotateWithDryRun performs decrypt+re-encrypt for all four secret types
// inside a single transaction. If dryRun is true the transaction is rolled back
// so no changes are persisted; the returned counts still reflect what would
// have been written.
func rotateWithDryRun(ctx context.Context, store *db.Store, oldKey, newKey []byte, dryRun bool) (providerCount, compatCount, webhookCount, mcpHeaderCount int, err error) {
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("begin rotation transaction: %w", err)
	}
	// Rollback is a no-op after Commit, so this deferred call is always safe.
	defer tx.Rollback() //nolint:errcheck

	q := store.Queries().WithTx(tx)
	now := time.Now().UTC().Format(time.RFC3339)

	// 1. system_settings rows whose key ends in "_api_key".
	apiKeyRows, err := q.ListAPIKeySystemSettings(ctx)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("list api key settings: %w", err)
	}
	for _, row := range apiKeyRows {
		// plaintext cannot be zeroed (Go string is immutable); key bytes are zeroed by the caller
		plaintext, err := admin.Decrypt(oldKey, row.Value)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("decrypt system_settings[%s]: %w", row.Key, err)
		}
		newCiphertext, err := admin.Encrypt(newKey, plaintext)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("re-encrypt system_settings[%s]: %w", row.Key, err)
		}
		if err := q.UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{
			Key:       row.Key,
			Value:     newCiphertext,
			UpdatedAt: now,
		}); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("write system_settings[%s]: %w", row.Key, err)
		}
		providerCount++
	}

	// 2. openai_compat_providers.api_key_encrypted.
	compatRows, err := q.ListOpenAICompatProviders(ctx)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("list openai-compat providers: %w", err)
	}
	for _, row := range compatRows {
		plaintext, err := admin.Decrypt(oldKey, row.ApiKeyEncrypted)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("decrypt openai_compat_providers id=%d name=%q: %w", row.ID, row.Name, err)
		}
		newCiphertext, err := admin.Encrypt(newKey, plaintext)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("re-encrypt openai_compat_providers id=%d name=%q: %w", row.ID, row.Name, err)
		}
		if err := q.UpdateOpenAICompatProviderAPIKey(ctx, db.UpdateOpenAICompatProviderAPIKeyParams{
			ApiKeyEncrypted: newCiphertext,
			UpdatedAt:       now,
			ID:              row.ID,
		}); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("write openai_compat_providers id=%d: %w", row.ID, err)
		}
		compatCount++
	}

	// 3. policies.webhook_secret_encrypted (non-NULL rows only).
	webhookRows, err := q.ListPolicyWebhookSecrets(ctx)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("list policy webhook secrets: %w", err)
	}
	for _, row := range webhookRows {
		// The query filters WHERE webhook_secret_encrypted IS NOT NULL, so this
		// pointer is always non-nil here. We guard anyway to be explicit.
		if row.WebhookSecretEncrypted == nil {
			continue
		}
		plaintext, err := admin.Decrypt(oldKey, *row.WebhookSecretEncrypted)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("decrypt policies.webhook_secret_encrypted id=%s: %w", row.ID, err)
		}
		newCiphertext, err := admin.Encrypt(newKey, plaintext)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("re-encrypt policies.webhook_secret_encrypted id=%s: %w", row.ID, err)
		}
		if err := q.SetPolicyWebhookSecret(ctx, db.SetPolicyWebhookSecretParams{
			Ciphertext: &newCiphertext,
			UpdatedAt:  now,
			ID:         row.ID,
		}); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("write policies.webhook_secret_encrypted id=%s: %w", row.ID, err)
		}
		webhookCount++
	}

	// 4. mcp_servers.auth_headers_encrypted (non-NULL rows only).
	mcpRows, err := q.ListMCPServersWithAuthHeaders(ctx)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("list mcp server auth headers: %w", err)
	}
	for _, row := range mcpRows {
		if row.AuthHeadersEncrypted == nil {
			continue
		}
		plaintext, err := admin.Decrypt(oldKey, *row.AuthHeadersEncrypted)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("decrypt mcp_servers.auth_headers_encrypted id=%s: %w", row.ID, err)
		}
		newCiphertext, err := admin.Encrypt(newKey, plaintext)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("re-encrypt mcp_servers.auth_headers_encrypted id=%s: %w", row.ID, err)
		}
		if err := q.UpdateMCPServerAuthHeaders(ctx, db.UpdateMCPServerAuthHeadersParams{
			AuthHeadersEncrypted: &newCiphertext,
			ID:                   row.ID,
		}); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("write mcp_servers.auth_headers_encrypted id=%s: %w", row.ID, err)
		}
		mcpHeaderCount++
	}

	if dryRun {
		// Intentional rollback: validate the old key covers all ciphertexts
		// without persisting any changes.
		return providerCount, compatCount, webhookCount, mcpHeaderCount, nil
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("commit rotation: %w", err)
	}
	return providerCount, compatCount, webhookCount, mcpHeaderCount, nil
}

// isBusy reports whether err is an SQLITE_BUSY error from the modernc.org/sqlite
// driver. The driver surfaces the SQLite error mnemonic in the error text.
func isBusy(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "sqlite_busy")
}

// Package rotatekey implements the `gleipnir rotate-key` subcommand.
//
// It re-encrypts every at-rest secret in the database (provider API keys in
// system_settings, OpenAI-compat provider keys, and per-policy webhook
// secrets) under a new AES-256-GCM key in a single transaction. Run this
// command with the server stopped; the command refuses to proceed if another
// process is holding the database write lock.
package rotatekey

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/db"
)

const defaultDBPath = "/data/gleipnir.db"

// Run is the public entry point for the rotate-key subcommand. args are
// already stripped of the leading "rotate-key" token. It writes human-readable
// output to out and error messages to errOut, then returns a shell exit code:
//   - 0  success
//   - 1  unexpected error (I/O, DB)
//   - 2  user error (bad flags, equal keys, invalid key format)
//   - 3  DB held by another process
func Run(args []string, out, errOut io.Writer) int {
	return runWithIO(args, os.Stdin, out, errOut)
}

// RunWithIO is like Run but accepts an explicit stdin reader. It is exported
// for integration tests that need to inject key material via stdin without
// touching the real os.Stdin.
func RunWithIO(args []string, stdin io.Reader, out, errOut io.Writer) int {
	return runWithIO(args, stdin, out, errOut)
}

// runWithIO is the testable core: it accepts an explicit stdin reader so tests
// can inject key material without touching the real stdin.
func runWithIO(args []string, stdin io.Reader, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("rotate-key", flag.ContinueOnError)
	fs.SetOutput(errOut)

	var oldKeyFlag, newKeyFlag, dbPath string
	var dryRun bool

	fs.StringVar(&oldKeyFlag, "old", "", "current encryption key (hex or base64; use \"-\" to read from stdin)")
	fs.StringVar(&newKeyFlag, "new", "", "new encryption key (hex or base64; use \"-\" to read from stdin)")
	fs.BoolVar(&dryRun, "dry-run", false, "decrypt and re-encrypt in memory but roll back; validates the old key covers every ciphertext")

	// Resolve default DB path: read env var, fall back to hardcoded default.
	envDBPath := os.Getenv("GLEIPNIR_DB_PATH")
	if envDBPath == "" {
		envDBPath = defaultDBPath
	}
	fs.StringVar(&dbPath, "db-path", envDBPath, "path to the SQLite database file")

	if err := fs.Parse(args); err != nil {
		// flag already wrote the error to errOut
		return 2
	}

	if oldKeyFlag == "" {
		fmt.Fprintln(errOut, "error: --old is required")
		return 2
	}
	if newKeyFlag == "" {
		fmt.Fprintln(errOut, "error: --new is required")
		return 2
	}

	// Read key material from stdin when either flag is "-". If both are "-",
	// we read old first, then new (one whitespace-trimmed line each).
	stdinReader := bufio.NewReader(stdin)
	if oldKeyFlag == "-" {
		line, err := stdinReader.ReadString('\n')
		if err != nil && err != io.EOF {
			fmt.Fprintf(errOut, "error: read --old from stdin: %v\n", err)
			return 1
		}
		oldKeyFlag = strings.TrimSpace(line)
	}
	if newKeyFlag == "-" {
		line, err := stdinReader.ReadString('\n')
		if err != nil && err != io.EOF {
			fmt.Fprintf(errOut, "error: read --new from stdin: %v\n", err)
			return 1
		}
		newKeyFlag = strings.TrimSpace(line)
	}

	oldKey, err := admin.ParseEncryptionKey(oldKeyFlag)
	if err != nil {
		fmt.Fprintf(errOut, "error: parse --old key: %v\n", err)
		return 2
	}

	newKey, err := admin.ParseEncryptionKey(newKeyFlag)
	if err != nil {
		fmt.Fprintf(errOut, "error: parse --new key: %v\n", err)
		return 2
	}

	if bytes.Equal(oldKey, newKey) {
		fmt.Fprintln(errOut, "error: new key equals old key; nothing to do")
		return 2
	}

	ctx := context.Background()

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

	providerCount, compatCount, webhookCount, rotErr := rotateWithDryRun(ctx, store, oldKey, newKey, dryRun)
	if rotErr != nil {
		fmt.Fprintf(errOut, "error: %v\n", rotErr)
		return 1
	}

	summary := fmt.Sprintf("re-encrypted %d provider keys, %d openai-compat keys, %d webhook secrets", providerCount, compatCount, webhookCount)
	if dryRun {
		summary += " (dry-run; no changes written)"
	}
	fmt.Fprintln(out, summary)
	return 0
}

// rotateWithDryRun performs decrypt+re-encrypt for all three secret types
// inside a single transaction. If dryRun is true the transaction is rolled back
// so no changes are persisted; the returned counts still reflect what would
// have been written.
func rotateWithDryRun(ctx context.Context, store *db.Store, oldKey, newKey []byte, dryRun bool) (providerCount, compatCount, webhookCount int, err error) {
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("begin rotation transaction: %w", err)
	}
	// Rollback is a no-op after Commit, so this deferred call is always safe.
	defer tx.Rollback() //nolint:errcheck

	q := store.Queries().WithTx(tx)
	now := time.Now().UTC().Format(time.RFC3339)

	// 1. system_settings rows whose key ends in "_api_key".
	apiKeyRows, err := q.ListAPIKeySystemSettings(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list api key settings: %w", err)
	}
	for _, row := range apiKeyRows {
		plaintext, err := admin.Decrypt(oldKey, row.Value)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("decrypt system_settings[%s]: %w", row.Key, err)
		}
		newCiphertext, err := admin.Encrypt(newKey, plaintext)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("re-encrypt system_settings[%s]: %w", row.Key, err)
		}
		if err := q.UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{
			Key:       row.Key,
			Value:     newCiphertext,
			UpdatedAt: now,
		}); err != nil {
			return 0, 0, 0, fmt.Errorf("write system_settings[%s]: %w", row.Key, err)
		}
		providerCount++
	}

	// 2. openai_compat_providers.api_key_encrypted.
	compatRows, err := q.ListOpenAICompatProviders(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list openai-compat providers: %w", err)
	}
	for _, row := range compatRows {
		plaintext, err := admin.Decrypt(oldKey, row.ApiKeyEncrypted)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("decrypt openai_compat_providers id=%d name=%q: %w", row.ID, row.Name, err)
		}
		newCiphertext, err := admin.Encrypt(newKey, plaintext)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("re-encrypt openai_compat_providers id=%d name=%q: %w", row.ID, row.Name, err)
		}
		if err := q.UpdateOpenAICompatProviderAPIKey(ctx, db.UpdateOpenAICompatProviderAPIKeyParams{
			ApiKeyEncrypted: newCiphertext,
			UpdatedAt:       now,
			ID:              row.ID,
		}); err != nil {
			return 0, 0, 0, fmt.Errorf("write openai_compat_providers id=%d: %w", row.ID, err)
		}
		compatCount++
	}

	// 3. policies.webhook_secret_encrypted (non-NULL rows only).
	webhookRows, err := q.ListPolicyWebhookSecrets(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list policy webhook secrets: %w", err)
	}
	for _, row := range webhookRows {
		// The query filters WHERE webhook_secret_encrypted IS NOT NULL, so this
		// pointer is always non-nil here. We guard anyway to be explicit.
		if row.WebhookSecretEncrypted == nil {
			continue
		}
		plaintext, err := admin.Decrypt(oldKey, *row.WebhookSecretEncrypted)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("decrypt policies.webhook_secret_encrypted id=%s: %w", row.ID, err)
		}
		newCiphertext, err := admin.Encrypt(newKey, plaintext)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("re-encrypt policies.webhook_secret_encrypted id=%s: %w", row.ID, err)
		}
		if err := q.SetPolicyWebhookSecret(ctx, db.SetPolicyWebhookSecretParams{
			Ciphertext: &newCiphertext,
			UpdatedAt:  now,
			ID:         row.ID,
		}); err != nil {
			return 0, 0, 0, fmt.Errorf("write policies.webhook_secret_encrypted id=%s: %w", row.ID, err)
		}
		webhookCount++
	}

	if dryRun {
		// Intentional rollback: validate the old key covers all ciphertexts
		// without persisting any changes.
		return providerCount, compatCount, webhookCount, nil
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, 0, fmt.Errorf("commit rotation: %w", err)
	}
	return providerCount, compatCount, webhookCount, nil
}

// isBusy reports whether err is an SQLITE_BUSY error from the modernc.org/sqlite
// driver. The driver surfaces the SQLite error mnemonic in the error text.
func isBusy(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "sqlite_busy")
}

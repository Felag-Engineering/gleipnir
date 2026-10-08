// Package main implements the gleipnirctl local admin CLI.
//
// rotate.go contains the core rotation logic: Rotate re-encrypts every
// at-rest secret in the database (every column listed by encryptedColumns in
// secrets.go) under a new AES-256-GCM key in a single transaction. The Cobra command wiring lives in
// rotatekey.go. Run this operation with the server stopped; Rotate refuses to
// proceed if another process holds the database write lock.
package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
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

	counts, rotErr := rotateWithDryRun(ctx, store, oldKey, newKey, dryRun)
	if rotErr != nil {
		fmt.Fprintf(errOut, "error: %v\n", rotErr)
		return 1
	}

	summary := fmt.Sprintf("re-encrypted %d provider keys, %d openai-compat keys, %d webhook secrets, %d MCP auth header sets, %d plugin credential sets",
		counts[colProviderKeys], counts[colOpenAICompatKeys], counts[colWebhookSecrets], counts[colMCPAuthHeaders], counts[colPluginCreds])
	if dryRun {
		summary += " (dry-run; no changes written)"
	}
	fmt.Fprintln(out, summary)
	return 0
}

// rotateWithDryRun performs decrypt+re-encrypt for every encrypted column
// (see encryptedColumns) inside a single transaction. If dryRun is true the
// transaction is rolled back so no changes are persisted; the returned counts,
// keyed by "table.column", still reflect what would have been written.
func rotateWithDryRun(ctx context.Context, store *db.Store, oldKey, newKey []byte, dryRun bool) (map[string]int, error) {
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin rotation transaction: %w", err)
	}
	// Rollback is a no-op after Commit, so this deferred call is always safe.
	defer tx.Rollback() //nolint:errcheck

	q := store.Queries().WithTx(tx)
	now := time.Now().UTC().Format(time.RFC3339)
	counts := make(map[string]int)

	for _, col := range encryptedColumns() {
		rows, err := col.list(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			// plaintext cannot be zeroed (Go string is immutable); key bytes are zeroed by the caller
			plaintext, err := crypto.Decrypt(oldKey, row.Ciphertext)
			if err != nil {
				return nil, fmt.Errorf("decrypt %s %s: %w", col.name(), row.ID, err)
			}
			newCiphertext, err := crypto.Encrypt(newKey, plaintext)
			if err != nil {
				return nil, fmt.Errorf("re-encrypt %s %s: %w", col.name(), row.ID, err)
			}
			if err := row.rewrite(ctx, q, newCiphertext, now); err != nil {
				return nil, fmt.Errorf("write %s %s: %w", col.name(), row.ID, err)
			}
			counts[col.name()]++
		}
	}

	if dryRun {
		// Intentional rollback: validate the old key covers all ciphertexts
		// without persisting any changes.
		return counts, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit rotation: %w", err)
	}
	return counts, nil
}

// isBusy reports whether err is an SQLITE_BUSY error from the modernc.org/sqlite
// driver. The driver surfaces the SQLite error mnemonic in the error text.
func isBusy(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "sqlite_busy")
}

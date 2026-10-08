package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
)

const encryptionKeyEnv = "GLEIPNIR_ENCRYPTION_KEY"

// secretFailure identifies one ciphertext that did not decrypt. It carries no
// ciphertext or plaintext, only where the row lives.
type secretFailure struct {
	Table  string
	Column string
	ID     string
}

// encryptionKeyFromEnv reads and parses GLEIPNIR_ENCRYPTION_KEY. The key is
// deliberately env-only: a flag would leak it into process listings and shell
// history.
func encryptionKeyFromEnv() ([]byte, error) {
	raw := os.Getenv(encryptionKeyEnv)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set", encryptionKeyEnv)
	}
	key, err := crypto.ParseEncryptionKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", encryptionKeyEnv, err)
	}
	return key, nil
}

// openReadOnly opens an existing SQLite file without any ability to write to
// it. Unlike db.Open it never creates a missing file, never switches the
// journal mode, and never migrates, so it is safe to point at a live database
// or at a path typed by mistake.
func openReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("stat db: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}).String()
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	return sqlDB, nil
}

// verifySecrets attempts to decrypt every ciphertext listed by
// encryptedColumns. It returns the number that decrypted and the identity of
// each one that did not. err is non-nil only when a column could not be listed
// (for example the table is missing).
func verifySecrets(ctx context.Context, q *db.Queries, key []byte) (verified int, failures []secretFailure, err error) {
	for _, col := range encryptedColumns() {
		rows, err := col.list(ctx, q)
		if err != nil {
			return 0, nil, err
		}
		for _, row := range rows {
			if _, err := crypto.Decrypt(key, row.Ciphertext); err != nil {
				failures = append(failures, secretFailure{Table: col.Table, Column: col.Column, ID: row.ID})
				continue
			}
			verified++
		}
	}
	return verified, failures, nil
}

// VerifyKeys checks that key decrypts every at-rest secret in the database at
// dbPath. It opens the database read-only and never migrates, so it is safe to
// run against a live server. Returns a shell exit code:
//
//	0 — every secret decrypted (including a database holding none)
//	1 — one or more secrets failed, or an unexpected error
func VerifyKeys(ctx context.Context, dbPath string, key []byte, out, errOut io.Writer) int {
	sqlDB, err := openReadOnly(dbPath)
	if err != nil {
		fmt.Fprintf(errOut, "error: open db: %v\n", err)
		return 1
	}
	defer sqlDB.Close()

	verified, failures, err := verifySecrets(ctx, db.New(sqlDB), key)
	if err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}

	if len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintf(errOut, "FAILED %s.%s %s\n", f.Table, f.Column, f.ID)
		}
		fmt.Fprintf(errOut, "error: %d secrets failed to decrypt (%d verified OK)\n", len(failures), verified)
		return 1
	}

	fmt.Fprintf(out, "verified %d secrets OK\n", verified)
	return 0
}

package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/db/migrations"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
)

type checkStatus string

const (
	checkPass checkStatus = "PASS"
	checkWarn checkStatus = "WARN"
	checkFail checkStatus = "FAIL"
	// checkSkip marks a check that could not run because a check it depends on
	// already failed. It never fails the run on its own; the root cause does.
	checkSkip checkStatus = "SKIP"
)

type checkResult struct {
	status checkStatus
	name   string
	detail string
}

// Check runs a set of read-only health checks against the database at dbPath
// and the encryption key in rawKey (the value of GLEIPNIR_ENCRYPTION_KEY, empty
// if unset), printing one PASS/WARN/FAIL/SKIP line per check to out. The
// database is opened read-only and is never migrated. Returns a shell exit
// code: 0 when no check failed (warnings do not fail the run), 1 otherwise.
//
// A missing admin user is a WARN, not a FAIL: the instance is healthy and
// serving, and a fresh install legitimately has no admin until first-run setup.
// The key-works check decrypts every stored secret (not a sample), because the
// whole set is a handful of rows and a sample would miss a single corrupt one.
func Check(ctx context.Context, dbPath, rawKey string, out io.Writer) int {
	var results []checkResult
	add := func(r checkResult) { results = append(results, r) }

	key, keyResults := checkEncryptionKey(rawKey)
	results = append(results, keyResults...)

	sqlDB, dbResult := checkDBReachable(ctx, dbPath)
	add(dbResult)

	if sqlDB == nil {
		add(checkResult{checkSkip, "schema migrated", "database not reachable"})
		add(checkResult{checkSkip, "encryption key decrypts stored secrets", "database not reachable"})
		add(checkResult{checkSkip, "active admin user", "database not reachable"})
	} else {
		defer sqlDB.Close()

		schemaResult := checkSchemaMigrated(ctx, sqlDB)
		add(schemaResult)

		if schemaResult.status != checkPass {
			add(checkResult{checkSkip, "encryption key decrypts stored secrets", "schema not migrated"})
			add(checkResult{checkSkip, "active admin user", "schema not migrated"})
		} else {
			q := db.New(sqlDB)
			if key == nil {
				add(checkResult{checkSkip, "encryption key decrypts stored secrets", "no usable encryption key"})
			} else {
				results = append(results, checkSecretsDecrypt(ctx, q, key)...)
			}
			add(checkAdminUser(ctx, q))
		}
	}

	failed := false
	for _, r := range results {
		fmt.Fprintf(out, "%s  %s", r.status, r.name)
		if r.detail != "" {
			fmt.Fprintf(out, ": %s", r.detail)
		}
		fmt.Fprintln(out)
		if r.status == checkFail {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

// checkEncryptionKey reports whether rawKey is present and parses as a 32-byte
// key. The returned key is nil unless both hold.
func checkEncryptionKey(rawKey string) ([]byte, []checkResult) {
	const name = "encryption key present and valid"
	if rawKey == "" {
		return nil, []checkResult{{checkFail, name, encryptionKeyEnv + " is not set"}}
	}
	key, err := crypto.ParseEncryptionKey(rawKey)
	if err != nil {
		return nil, []checkResult{{checkFail, name, err.Error()}}
	}
	return key, []checkResult{{checkPass, name, ""}}
}

// checkDBReachable opens the database read-only and runs a trivial query. On
// success the caller owns the returned handle; on failure it is nil.
func checkDBReachable(ctx context.Context, dbPath string) (*sql.DB, checkResult) {
	const name = "database reachable"
	sqlDB, err := openReadOnly(dbPath)
	if err != nil {
		return nil, checkResult{checkFail, name, err.Error()}
	}
	var one int
	if err := sqlDB.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		sqlDB.Close()
		return nil, checkResult{checkFail, name, err.Error()}
	}
	return sqlDB, checkResult{checkPass, name, dbPath}
}

// checkSchemaMigrated reports whether every registered migration is already
// applied, without applying anything.
func checkSchemaMigrated(ctx context.Context, sqlDB *sql.DB) checkResult {
	const name = "schema migrated"
	pending, err := pendingMigrations(ctx, sqlDB)
	if err != nil {
		return checkResult{checkFail, name, err.Error()}
	}
	if len(pending) > 0 {
		return checkResult{checkFail, name, fmt.Sprintf("%d pending: %v (start the server to apply)", len(pending), pending)}
	}
	return checkResult{checkPass, name, ""}
}

// pendingMigrations returns the names of registered migrations that have not
// been applied. Migrations are not recorded per-version; each one's ShouldSkip
// inspects the live schema, which is exactly how Store.Migrate decides, and is
// read-only. A migration without ShouldSkip (data-only fix-ups that are safe to
// re-run) cannot be detected and is not counted as pending. A probe that errors
// counts as pending: it is not provably applied.
func pendingMigrations(ctx context.Context, sqlDB *sql.DB) ([]string, error) {
	var count int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`,
	).Scan(&count); err != nil {
		return nil, fmt.Errorf("check schema_migrations: %w", err)
	}
	if count == 0 {
		return nil, fmt.Errorf("schema not initialised (no schema_migrations table)")
	}

	var pending []string
	for _, m := range migrations.All() {
		skipper, ok := m.(migrations.ShouldSkipper)
		if !ok {
			continue
		}
		applied, err := skipper.ShouldSkip(ctx, sqlDB)
		if err != nil {
			// Several ShouldSkip probes query tables an older schema lacks and
			// fail rather than return false. Not provably applied means pending.
			pending = append(pending, fmt.Sprintf("%d %s", m.Version(), m.Name()))
			continue
		}
		if !applied {
			pending = append(pending, fmt.Sprintf("%d %s", m.Version(), m.Name()))
		}
	}
	return pending, nil
}

// checkSecretsDecrypt reuses verifySecrets, so it covers exactly the columns
// rotate-key and verify-keys cover. Failing rows are listed as extra FAIL lines.
func checkSecretsDecrypt(ctx context.Context, q *db.Queries, key []byte) []checkResult {
	const name = "encryption key decrypts stored secrets"
	verified, failures, err := verifySecrets(ctx, q, key)
	if err != nil {
		return []checkResult{{checkFail, name, err.Error()}}
	}
	if len(failures) > 0 {
		results := []checkResult{{checkFail, name, fmt.Sprintf("%d failed, %d verified OK", len(failures), verified)}}
		for _, f := range failures {
			results = append(results, checkResult{checkFail, "  undecryptable secret", fmt.Sprintf("%s.%s %s", f.Table, f.Column, f.ID)})
		}
		return results
	}
	return []checkResult{{checkPass, name, fmt.Sprintf("%d secrets", verified)}}
}

func checkAdminUser(ctx context.Context, q *db.Queries) checkResult {
	const name = "active admin user"
	admins, err := q.ListActiveUsersByRole(ctx, "admin")
	if err != nil {
		return checkResult{checkFail, name, err.Error()}
	}
	if len(admins) == 0 {
		return checkResult{checkWarn, name, "none found; create one with gleipnirctl create-user"}
	}
	return checkResult{checkPass, name, fmt.Sprintf("%d", len(admins))}
}

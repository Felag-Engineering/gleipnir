package migrations_test

import (
	"context"
	"database/sql"
	_ "embed"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/rapp992/gleipnir/internal/db/migrations"
)

//go:embed 0001_initial.sql
var initialSchema string

// openTestDB opens a fresh SQLite database in a temp directory with WAL mode
// and foreign keys enabled, mirroring the setup in internal/db.Open.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	return db
}

// applyInitialSchema executes the initial schema SQL so that the Go migrations
// have a valid baseline to run against.
func applyInitialSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(initialSchema); err != nil {
		t.Fatalf("apply initial schema: %v", err)
	}
}

// TestAllMigrationsIdempotent applies every migration in All() twice against a
// fresh database that already has the full schema from 0001_initial.sql. Both
// runs must succeed without error — each migration must be a no-op when the
// target state already exists.
func TestAllMigrationsIdempotent(t *testing.T) {
	ctx := context.Background()

	for _, m := range migrations.All() {
		m := m // capture for parallel sub-test
		t.Run(m.Name(), func(t *testing.T) {
			t.Parallel()
			db := openTestDB(t)
			applyInitialSchema(t, db)

			ms := []migrations.Migration{m}

			if err := migrations.Apply(ctx, db, ms, nil); err != nil {
				t.Fatalf("first Apply: %v", err)
			}
			if err := migrations.Apply(ctx, db, ms, nil); err != nil {
				t.Fatalf("second Apply (idempotency): %v", err)
			}
		})
	}
}

// TestAllMigrationsOrdered verifies that applying all migrations in order
// against a fresh initial schema succeeds and that schema_migrations still
// contains version 1 afterwards.
func TestAllMigrationsOrdered(t *testing.T) {
	db := openTestDB(t)
	applyInitialSchema(t, db)

	if err := migrations.Apply(context.Background(), db, migrations.All(), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 1`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_migrations version 1 row missing after migrations")
	}
}

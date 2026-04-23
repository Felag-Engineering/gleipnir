package migrations_test

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
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

// TestFKToggleMigrationUpError verifies that when a FK-toggling migration's
// Up() returns an error the runner does not deadlock. The pool is capped at
// MaxOpenConns(1); without an explicit rollback before re-enabling FK the
// runner's db.ExecContext("PRAGMA foreign_keys=ON") would wait forever for a
// connection the open transaction is holding.
func TestFKToggleMigrationUpError(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	applyInitialSchema(t, db)

	boom := &failingFKMigration{}
	err := migrations.Apply(ctx, db, []migrations.Migration{boom}, nil)
	if err == nil {
		t.Fatal("expected Apply to return an error, got nil")
	}
}

// failingFKMigration is a FK-toggle migration whose Up() always fails.
type failingFKMigration struct{}

func (m *failingFKMigration) Version() int                 { return 999 }
func (m *failingFKMigration) Name() string                 { return "failing_fk_migration" }
func (m *failingFKMigration) RequiresForeignKeysOff() bool { return true }
func (m *failingFKMigration) Up(_ context.Context, _ *sql.Tx) error {
	return errors.New("intentional failure")
}

// TestDeleteUserPrefDefaultModel verifies that the migration removes only
// user_preferences rows with key = 'default_model' and leaves other keys alone.
func TestDeleteUserPrefDefaultModel(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	applyInitialSchema(t, db)

	// Insert a user to satisfy the foreign key constraint.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id, username, password_hash, created_at) VALUES ('u1', 'alice', 'x', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// Insert one default_model row (should be deleted) and one timezone row (should survive).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_preferences(user_id, preference_key, preference_value, updated_at)
		 VALUES ('u1', 'default_model', 'claude-x', '2024-01-01T00:00:00Z'),
		        ('u1', 'timezone', 'UTC', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert preferences: %v", err)
	}

	m := &migrations.DeleteUserPrefDefaultModel{}
	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Only the timezone row should remain.
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_preferences`).Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after migration, got %d", count)
	}

	var key string
	if err := db.QueryRowContext(ctx, `SELECT preference_key FROM user_preferences`).Scan(&key); err != nil {
		t.Fatalf("query key: %v", err)
	}
	if key != "timezone" {
		t.Errorf("surviving row key = %q, want %q", key, "timezone")
	}
}

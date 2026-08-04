package migrations_test

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/felag-engineering/gleipnir/internal/db/migrations"
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

// TestPendingReauthorizeRebuildPreservesChildFKRows is a regression test for
// issue #494: the 0035 migration lacked RequiresForeignKeysOff(), so running
// it against a live database with FK enforcement ON would cause SQLite to null
// out plugin_audit_events.plugin_instance_id (via ON DELETE SET NULL) when the
// old plugin_instances table was dropped. The fix adds the method; this test
// proves the child FK link survives.
//
// We cannot rely on applyInitialSchema here because 0001_initial.sql already
// contains 'pending_reauthorize' in the plugin_instances CHECK constraint, which
// would make ShouldSkip() return true and skip Up() entirely. Instead we
// hand-craft the pre-0035 baseline (the end-of-0034 shape) so the migration
// body actually runs.
func TestPendingReauthorizeRebuildPreservesChildFKRows(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// Build the pre-0035 schema by hand — do NOT call applyInitialSchema.
	seedPreReauthorizeBaseline(t, db)

	// Seed a plugins parent row, a plugin_instances row, and a child
	// plugin_audit_events row whose plugin_instance_id references 'inst-1'.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		VALUES ('plug-1', 'test-plugin', '1.0.0', '{}', 'pk', 'active', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert plugin: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, subscription_scope_json, handshake_versions, health_state, version, created_at, updated_at)
		VALUES ('inst-1', 'plug-1', 'my-instance', '{}', '{}', '{}', 'healthy', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert plugin_instance: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_audit_events(plugin_instance_id, event_type, severity, payload_json, created_at)
		VALUES ('inst-1', 'test_event', 'info', '{}', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert plugin_audit_event: %v", err)
	}

	// (d) Confirm ShouldSkip is false so the test fails loudly if a future schema
	// change makes the migration skip again (which would make the rest of this
	// test a vacuous pass).
	skip, err := (&migrations.AddPendingReauthorizeHealthState{}).ShouldSkip(ctx, db)
	if err != nil {
		t.Fatalf("ShouldSkip: %v", err)
	}
	if skip {
		t.Fatal("ShouldSkip returned true against the pre-target baseline — the hand-crafted DDL must omit 'pending_reauthorize'")
	}

	// (e) Confirm FK enforcement is ON before running so the test cannot go
	// vacuous if PRAGMA foreign_keys is silently off.
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d before Apply, want 1", fk)
	}

	// (f) Run the migration. Apply returning nil is NOT the regression gate —
	// SQLite executes the DROP TABLE without erroring even under FK ON (it fires
	// ON DELETE SET NULL on child rows instead). The gate is step (g) below.
	if err := migrations.Apply(ctx, db, []migrations.Migration{&migrations.AddPendingReauthorizeHealthState{}}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// (g) Assert post-migration state. These are the regression gates.

	// The new CHECK constraint must contain 'pending_reauthorize'.
	var ddl string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='plugin_instances'`,
	).Scan(&ddl); err != nil {
		t.Fatalf("read post-migration DDL: %v", err)
	}
	if !strings.Contains(ddl, "pending_reauthorize") {
		t.Errorf("post-migration plugin_instances DDL does not contain 'pending_reauthorize'")
	}

	// The child plugin_audit_events row must still reference 'inst-1'. Without
	// RequiresForeignKeysOff, DROP TABLE fires ON DELETE SET NULL and the value
	// becomes NULL — silent data corruption. This is the primary regression gate.
	var instanceID sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT plugin_instance_id FROM plugin_audit_events`,
	).Scan(&instanceID); err != nil {
		t.Fatalf("read plugin_audit_events: %v", err)
	}
	if !instanceID.Valid || instanceID.String != "inst-1" {
		t.Errorf("plugin_audit_events.plugin_instance_id = %v after migration, want 'inst-1' — FK link was corrupted (ON DELETE SET NULL fired during DROP TABLE)", instanceID)
	}

	// The plugin_instances row must have survived the rebuild.
	var rowCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_instances`).Scan(&rowCount); err != nil {
		t.Fatalf("count plugin_instances: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("plugin_instances row count = %d after migration, want 1", rowCount)
	}

	// FK enforcement must be back ON after Apply (guards the runner's re-enable path).
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys after Apply: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d after Apply, want 1", fk)
	}
}

// seedPreReauthorizeBaseline hand-creates the end-of-0034 schema shape that
// 0035 expects to find: schema_migrations, plugins, plugin_instances (WITHOUT
// 'pending_reauthorize' in its CHECK list), and plugin_audit_events.
//
// This is the only way to make 0035's Up() body actually execute, because
// 0001_initial.sql already ships 'pending_reauthorize' and would cause
// ShouldSkip to return true.
func seedPreReauthorizeBaseline(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		// schema_migrations — keep the version row so suite-wide invariants hold.
		`CREATE TABLE schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TEXT    NOT NULL
		)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (1, '2024-01-01T00:00:00Z')`,

		// plugins — parent table referenced by plugin_instances FK.
		`CREATE TABLE plugins (
			id                 TEXT    PRIMARY KEY,
			name               TEXT    NOT NULL UNIQUE,
			plugin_version     TEXT    NOT NULL,
			manifest_snapshot  TEXT    NOT NULL,
			trusted_pubkey     TEXT    NOT NULL,
			status             TEXT    NOT NULL CHECK(status IN ('pending_review','active','removed')),
			binary_path        TEXT,
			version            INTEGER NOT NULL DEFAULT 0,
			created_at         TEXT    NOT NULL,
			updated_at         TEXT    NOT NULL
		)`,
		`CREATE INDEX idx_plugins_status ON plugins(status)`,

		// plugin_instances — end-of-0034 shape: includes subscription_scope_json
		// (added by 0033) but OMITS both 'pending_reauthorize' and 'inactive' so
		// ShouldSkip returns false and Up() actually runs.
		`CREATE TABLE plugin_instances (
			id                       TEXT    PRIMARY KEY,
			plugin_id                TEXT    NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
			instance_name            TEXT    NOT NULL,
			config_json              TEXT    NOT NULL DEFAULT '{}',
			subscription_scope_json  TEXT    NOT NULL DEFAULT '{}',
			credentials_encrypted    TEXT,
			credentials_expires_at   TEXT,
			handshake_versions       TEXT    NOT NULL DEFAULT '{}',
			health_state             TEXT    NOT NULL DEFAULT 'pending_key_approval'
			                                 CHECK(health_state IN (
			                                     'healthy',
			                                     'signature_invalid',
			                                     'pending_key_approval',
			                                     'pending_manifest_approval',
			                                     'pending_config_migration',
			                                     'verification_error',
			                                     'unsigned_permissive',
			                                     'unhealthy',
			                                     'crashed',
			                                     'circuit_broken'
			                                 )),
			health_detail            TEXT,
			last_oauth_callback_url  TEXT,
			version                  INTEGER NOT NULL DEFAULT 0,
			created_at               TEXT    NOT NULL,
			updated_at               TEXT    NOT NULL,
			UNIQUE (plugin_id, instance_name)
		)`,
		`CREATE INDEX idx_plugin_instances_plugin_id    ON plugin_instances(plugin_id)`,
		`CREATE INDEX idx_plugin_instances_health_state ON plugin_instances(health_state)`,

		// plugin_audit_events — FK child with ON DELETE SET NULL; this is the
		// table whose plugin_instance_id we assert survives the rebuild.
		`CREATE TABLE plugin_audit_events (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			plugin_instance_id  TEXT    REFERENCES plugin_instances(id) ON DELETE SET NULL,
			event_type          TEXT    NOT NULL,
			severity            TEXT    NOT NULL CHECK(severity IN ('info','warning','high','critical')),
			actor_user_id       TEXT,
			payload_json        TEXT    NOT NULL,
			created_at          TEXT    NOT NULL
		)`,
		`CREATE INDEX idx_pae_instance_created ON plugin_audit_events(plugin_instance_id, created_at)`,
		`CREATE INDEX idx_pae_event_created    ON plugin_audit_events(event_type, created_at)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seedPreReauthorizeBaseline: %v\nstatement: %s", err, stmt)
		}
	}
}

// TestAddUserSlackUserID verifies that migration 0041 adds slack_user_id to the
// users table with a UNIQUE index, allows set+read round-trips, and rejects
// duplicate Slack ids.
func TestAddUserSlackUserID(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	applyInitialSchema(t, db)

	// Seed two users using the initial schema (no slack_user_id column yet).
	for _, u := range []struct{ id, username string }{
		{"u1", "alice"},
		{"u2", "bob"},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO users(id, username, password_hash, created_at) VALUES (?, ?, 'x', '2024-01-01T00:00:00Z')`,
			u.id, u.username,
		); err != nil {
			t.Fatalf("seed user %s: %v", u.id, err)
		}
	}

	m := &migrations.AddUserSlackUserID{}
	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Set alice's Slack id.
	if _, err := db.ExecContext(ctx,
		`UPDATE users SET slack_user_id = 'U-alice-slack' WHERE id = 'u1'`,
	); err != nil {
		t.Fatalf("set alice slack_user_id: %v", err)
	}

	// Read it back.
	var slackID string
	if err := db.QueryRowContext(ctx, `SELECT slack_user_id FROM users WHERE id = 'u1'`).Scan(&slackID); err != nil {
		t.Fatalf("read slack_user_id: %v", err)
	}
	if slackID != "U-alice-slack" {
		t.Errorf("slack_user_id = %q, want %q", slackID, "U-alice-slack")
	}

	// Try to assign the same Slack id to bob — must fail (UNIQUE).
	_, err := db.ExecContext(ctx,
		`UPDATE users SET slack_user_id = 'U-alice-slack' WHERE id = 'u2'`,
	)
	if err == nil {
		t.Fatal("expected UNIQUE constraint error assigning duplicate slack_user_id, got nil")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("expected UNIQUE error, got: %v", err)
	}

	// NULL (cleared) slack_user_id does not violate the unique index; both users
	// can have NULL simultaneously (SQLite NULLs are distinct in UNIQUE indexes).
	if _, err := db.ExecContext(ctx, `UPDATE users SET slack_user_id = NULL WHERE id = 'u1'`); err != nil {
		t.Fatalf("clear alice slack_user_id: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET slack_user_id = NULL WHERE id = 'u2'`); err != nil {
		t.Fatalf("clear bob slack_user_id: %v", err)
	}
}

// seedPreProtocolVersionMCPServers hand-creates the pre-0042 schema shape:
// schema_migrations plus an mcp_servers table WITHOUT protocol_version. This
// is the only way to make 0042's Up() body actually execute, because
// 0001_initial.sql already ships protocol_version and would cause ShouldSkip
// to return true (see the equivalent note on seedPreReauthorizeBaseline).
func seedPreProtocolVersionMCPServers(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TEXT    NOT NULL
		)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (1, '2024-01-01T00:00:00Z')`,

		// mcp_servers — end-of-0041 shape: OMITS protocol_version so ShouldSkip
		// returns false and Up() actually runs.
		`CREATE TABLE mcp_servers (
			id                      TEXT    PRIMARY KEY,
			name                    TEXT    NOT NULL UNIQUE,
			url                     TEXT    NOT NULL,
			last_discovered_at      TEXT,
			has_drift               INTEGER NOT NULL DEFAULT 0,
			created_at              TEXT    NOT NULL,
			auth_headers_encrypted  TEXT
		)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seedPreProtocolVersionMCPServers: %v\nstatement: %s", err, stmt)
		}
	}
}

// TestAddMCPServerProtocolVersion verifies that migration 0042 adds
// protocol_version to mcp_servers on the existing-database upgrade path,
// leaves pre-existing rows NULL ("not yet probed"), and round-trips values.
func TestAddMCPServerProtocolVersion(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// Build the pre-0042 schema by hand — do NOT call applyInitialSchema.
	seedPreProtocolVersionMCPServers(t, db)

	// Confirm ShouldSkip is false so the test fails loudly if a future schema
	// change makes the migration skip again (which would make the rest of this
	// test a vacuous pass).
	m := &migrations.AddMCPServerProtocolVersion{}
	skip, err := m.ShouldSkip(ctx, db)
	if err != nil {
		t.Fatalf("ShouldSkip: %v", err)
	}
	if skip {
		t.Fatal("ShouldSkip returned true against the pre-target baseline — the hand-crafted DDL must omit protocol_version")
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO mcp_servers(id, name, url, created_at) VALUES ('s1', 'srv', 'http://localhost:8080', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed mcp_servers row: %v", err)
	}

	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var count int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('mcp_servers') WHERE name = 'protocol_version'`,
	).Scan(&count); err != nil {
		t.Fatalf("query pragma_table_info: %v", err)
	}
	if count != 1 {
		t.Fatalf("protocol_version column count = %d, want 1", count)
	}

	// The pre-existing row must backfill to NULL, not some default.
	var pv sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT protocol_version FROM mcp_servers WHERE id = 's1'`).Scan(&pv); err != nil {
		t.Fatalf("read protocol_version: %v", err)
	}
	if pv.Valid {
		t.Errorf("protocol_version = %q after migration, want NULL", pv.String)
	}

	// Round-trip: set, read back, clear, read back.
	if _, err := db.ExecContext(ctx, `UPDATE mcp_servers SET protocol_version = '2026-07-28' WHERE id = 's1'`); err != nil {
		t.Fatalf("set protocol_version: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT protocol_version FROM mcp_servers WHERE id = 's1'`).Scan(&pv); err != nil {
		t.Fatalf("read protocol_version after set: %v", err)
	}
	if !pv.Valid || pv.String != "2026-07-28" {
		t.Errorf("protocol_version = %v after set, want '2026-07-28'", pv)
	}
	if _, err := db.ExecContext(ctx, `UPDATE mcp_servers SET protocol_version = NULL WHERE id = 's1'`); err != nil {
		t.Fatalf("clear protocol_version: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT protocol_version FROM mcp_servers WHERE id = 's1'`).Scan(&pv); err != nil {
		t.Fatalf("read protocol_version after clear: %v", err)
	}
	if pv.Valid {
		t.Errorf("protocol_version = %q after clear, want NULL", pv.String)
	}

	// Second Apply must be a no-op — proves ShouldSkip flips to true after Up,
	// not just on a fresh schema.
	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("second Apply (idempotency): %v", err)
	}
}

// TestAddMCPServerProtocolVersionSkipsOnFreshSchema is a regression gate for
// forgetting to hand-sync 0001_initial.sql: on a fresh database built from the
// initial schema, protocol_version must already exist, so ShouldSkip must
// return true. If someone adds the Go migration without editing
// 0001_initial.sql, this test fails (and sqlc would silently not see the
// column, since sqlc.yaml only reads 0001_initial.sql).
func TestAddMCPServerProtocolVersionSkipsOnFreshSchema(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	applyInitialSchema(t, db)

	skip, err := (&migrations.AddMCPServerProtocolVersion{}).ShouldSkip(ctx, db)
	if err != nil {
		t.Fatalf("ShouldSkip: %v", err)
	}
	if !skip {
		t.Fatal("ShouldSkip returned false on a fresh initial schema — did 0001_initial.sql forget the protocol_version column?")
	}
}

// seedPreCanonicalSchemaMCPTools hand-creates the end-of-0042 schema shape:
// schema_migrations plus an mcp_tools table WITHOUT canonical_schema. This is
// the only way to make 0043's Up() body actually execute, because
// 0001_initial.sql already ships canonical_schema and would cause ShouldSkip
// to return true (see the equivalent note on seedPreProtocolVersionMCPServers).
// No FK to mcp_servers is needed — the seed is self-contained.
func seedPreCanonicalSchemaMCPTools(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TEXT    NOT NULL
		)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (1, '2024-01-01T00:00:00Z')`,

		// mcp_tools — end-of-0042 shape: OMITS canonical_schema so ShouldSkip
		// returns false and Up() actually runs.
		`CREATE TABLE mcp_tools (
			id              TEXT    PRIMARY KEY,
			server_id       TEXT    NOT NULL,
			name            TEXT    NOT NULL,
			description     TEXT    NOT NULL,
			input_schema    TEXT    NOT NULL,
			created_at      TEXT    NOT NULL,
			enabled         INTEGER NOT NULL DEFAULT 1,
			UNIQUE(server_id, name)
		)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seedPreCanonicalSchemaMCPTools: %v\nstatement: %s", err, stmt)
		}
	}
}

// TestAddMCPToolCanonicalSchema verifies that migration 0043 adds
// canonical_schema to mcp_tools on the existing-database upgrade path, leaves
// pre-existing rows NULL ("no canonical form stored"), and round-trips values.
func TestAddMCPToolCanonicalSchema(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// Build the pre-0043 schema by hand — do NOT call applyInitialSchema.
	seedPreCanonicalSchemaMCPTools(t, db)

	// Confirm ShouldSkip is false so the test fails loudly if a future schema
	// change makes the migration skip again (which would make the rest of this
	// test a vacuous pass).
	m := &migrations.AddMCPToolCanonicalSchema{}
	skip, err := m.ShouldSkip(ctx, db)
	if err != nil {
		t.Fatalf("ShouldSkip: %v", err)
	}
	if skip {
		t.Fatal("ShouldSkip returned true against the pre-target baseline — the hand-crafted DDL must omit canonical_schema")
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO mcp_tools(id, server_id, name, description, input_schema, created_at) VALUES ('t1', 's1', 'tool-a', 'desc', '{}', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed mcp_tools row: %v", err)
	}

	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var count int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('mcp_tools') WHERE name = 'canonical_schema'`,
	).Scan(&count); err != nil {
		t.Fatalf("query pragma_table_info: %v", err)
	}
	if count != 1 {
		t.Fatalf("canonical_schema column count = %d, want 1", count)
	}

	// The pre-existing row must backfill to NULL, not some default.
	var cs sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT canonical_schema FROM mcp_tools WHERE id = 't1'`).Scan(&cs); err != nil {
		t.Fatalf("read canonical_schema: %v", err)
	}
	if cs.Valid {
		t.Errorf("canonical_schema = %q after migration, want NULL", cs.String)
	}

	// Round-trip: set, read back, clear, read back.
	if _, err := db.ExecContext(ctx, `UPDATE mcp_tools SET canonical_schema = '{}' WHERE id = 't1'`); err != nil {
		t.Fatalf("set canonical_schema: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT canonical_schema FROM mcp_tools WHERE id = 't1'`).Scan(&cs); err != nil {
		t.Fatalf("read canonical_schema after set: %v", err)
	}
	if !cs.Valid || cs.String != "{}" {
		t.Errorf("canonical_schema = %v after set, want '{}'", cs)
	}
	if _, err := db.ExecContext(ctx, `UPDATE mcp_tools SET canonical_schema = NULL WHERE id = 't1'`); err != nil {
		t.Fatalf("clear canonical_schema: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT canonical_schema FROM mcp_tools WHERE id = 't1'`).Scan(&cs); err != nil {
		t.Fatalf("read canonical_schema after clear: %v", err)
	}
	if cs.Valid {
		t.Errorf("canonical_schema = %q after clear, want NULL", cs.String)
	}

	// Second Apply must be a no-op — proves ShouldSkip flips to true after Up,
	// not just on a fresh schema.
	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("second Apply (idempotency): %v", err)
	}
}

// TestAddMCPToolCanonicalSchemaSkipsOnFreshSchema is a regression gate for
// forgetting to hand-sync 0001_initial.sql: on a fresh database built from the
// initial schema, canonical_schema must already exist, so ShouldSkip must
// return true. If someone adds the Go migration without editing
// 0001_initial.sql, this test fails (and sqlc would silently not see the
// column, since sqlc.yaml only reads 0001_initial.sql).
func TestAddMCPToolCanonicalSchemaSkipsOnFreshSchema(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	applyInitialSchema(t, db)

	skip, err := (&migrations.AddMCPToolCanonicalSchema{}).ShouldSkip(ctx, db)
	if err != nil {
		t.Fatalf("ShouldSkip: %v", err)
	}
	if !skip {
		t.Fatal("ShouldSkip returned false on a fresh initial schema — did 0001_initial.sql forget the canonical_schema column?")
	}
}

// seedPreToolInputRequestsBaseline hand-creates the end-of-0043 schema shape:
// schema_migrations plus the minimal set of parent tables (policies, runs,
// mcp_servers) that tool_input_requests and mcp_tasks reference by foreign
// key, WITHOUT tool_input_requests or mcp_tasks themselves. This is the only
// way to make 0044's Up() body actually execute, because 0001_initial.sql
// already ships both tables and would cause ShouldSkip to return true (see
// the equivalent note on seedPreProtocolVersionMCPServers).
func seedPreToolInputRequestsBaseline(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TEXT    NOT NULL
		)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (1, '2024-01-01T00:00:00Z')`,

		`CREATE TABLE policies (
			id           TEXT NOT NULL PRIMARY KEY,
			name         TEXT NOT NULL UNIQUE,
			trigger_type TEXT NOT NULL,
			yaml         TEXT NOT NULL,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		)`,

		`CREATE TABLE runs (
			id              TEXT NOT NULL PRIMARY KEY,
			policy_id       TEXT NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
			status          TEXT NOT NULL,
			trigger_type    TEXT NOT NULL,
			trigger_payload TEXT NOT NULL,
			started_at      TEXT NOT NULL,
			created_at      TEXT NOT NULL
		)`,

		`CREATE TABLE mcp_servers (
			id         TEXT NOT NULL PRIMARY KEY,
			name       TEXT NOT NULL UNIQUE,
			url        TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seedPreToolInputRequestsBaseline: %v\nstatement: %s", err, stmt)
		}
	}
}

// TestAddToolInputRequestsAndMCPTasks verifies that migration 0044 creates
// both tool_input_requests and mcp_tasks on the existing-database upgrade
// path, that rows referencing the pre-seeded runs/mcp_servers parents
// round-trip, and that the migration is idempotent.
func TestAddToolInputRequestsAndMCPTasks(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// Build the pre-0044 schema by hand — do NOT call applyInitialSchema.
	seedPreToolInputRequestsBaseline(t, db)

	m := &migrations.AddToolInputRequestsAndMCPTasks{}

	// Confirm ShouldSkip is false so the test fails loudly if a future schema
	// change makes the migration skip again (which would make the rest of
	// this test a vacuous pass).
	skip, err := m.ShouldSkip(ctx, db)
	if err != nil {
		t.Fatalf("ShouldSkip: %v", err)
	}
	if skip {
		t.Fatal("ShouldSkip returned true against the pre-target baseline — the hand-crafted DDL must omit tool_input_requests and mcp_tasks")
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO policies(id, name, trigger_type, yaml, created_at, updated_at) VALUES ('p1', 'policy-1', 'manual', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at) VALUES ('r1', 'p1', 'running', 'manual', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO mcp_servers(id, name, url, created_at) VALUES ('s1', 'srv', 'http://localhost:8080', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed mcp_servers row: %v", err)
	}

	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var tableCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('tool_input_requests', 'mcp_tasks')`,
	).Scan(&tableCount); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if tableCount != 2 {
		t.Fatalf("table count = %d, want 2 (tool_input_requests, mcp_tasks)", tableCount)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO tool_input_requests
			(id, run_id, server_id, tool_name, call_args, request_state, request_payload, elicitation_kind, status, expires_at, created_at)
		 VALUES
			('tir1', 'r1', 's1', 'send_invoice', '{}', 'state', '{}', 'permission', 'pending', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert tool_input_requests row: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO mcp_tasks
			(id, run_id, server_id, task_id, kind, status, created_at, updated_at)
		 VALUES
			('task1', 'r1', 's1', 'remote-task-1', 'tool_call', 'working', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert mcp_tasks row: %v", err)
	}

	// Second Apply must be a no-op — proves ShouldSkip flips to true after
	// Up, not just on a fresh schema.
	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("second Apply (idempotency): %v", err)
	}
}

// TestAddToolInputRequestsAndMCPTasksSkipsOnFreshSchema is a regression gate
// for forgetting to hand-sync 0001_initial.sql: on a fresh database built
// from the initial schema, tool_input_requests and mcp_tasks must already
// exist, so ShouldSkip must return true. If someone adds the Go migration
// without editing 0001_initial.sql, this test fails (and sqlc would silently
// not see the tables, since sqlc.yaml only reads 0001_initial.sql).
func TestAddToolInputRequestsAndMCPTasksSkipsOnFreshSchema(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	applyInitialSchema(t, db)

	skip, err := (&migrations.AddToolInputRequestsAndMCPTasks{}).ShouldSkip(ctx, db)
	if err != nil {
		t.Fatalf("ShouldSkip: %v", err)
	}
	if !skip {
		t.Fatal("ShouldSkip returned false on a fresh initial schema — did 0001_initial.sql forget the tool_input_requests/mcp_tasks tables?")
	}
}

// seedPreContainerSubstrateBaseline hand-creates the end-of-0044 schema shape
// as far as 0045 cares about it: schema_migrations plus the two parent tables
// (plugins, plugin_instances) the container tables reference by foreign key,
// WITHOUT any of the four container tables. Hand-crafting is the only way to
// make 0045's Up() body actually execute, because 0001_initial.sql already
// ships those tables and would make ShouldSkip return true (same note as
// seedPreToolInputRequestsBaseline).
func seedPreContainerSubstrateBaseline(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TEXT    NOT NULL
		)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (1, '2024-01-01T00:00:00Z')`,

		`CREATE TABLE plugins (
			id                TEXT NOT NULL PRIMARY KEY,
			name              TEXT NOT NULL UNIQUE,
			plugin_version    TEXT NOT NULL,
			manifest_snapshot TEXT NOT NULL,
			trusted_pubkey    TEXT NOT NULL,
			status            TEXT NOT NULL,
			version           INTEGER NOT NULL DEFAULT 0,
			created_at        TEXT NOT NULL,
			updated_at        TEXT NOT NULL
		)`,

		`CREATE TABLE plugin_instances (
			id            TEXT NOT NULL PRIMARY KEY,
			plugin_id     TEXT NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
			instance_name TEXT NOT NULL,
			config_json   TEXT NOT NULL DEFAULT '{}',
			health_state  TEXT NOT NULL,
			version       INTEGER NOT NULL DEFAULT 0,
			created_at    TEXT NOT NULL,
			updated_at    TEXT NOT NULL,
			UNIQUE (plugin_id, instance_name)
		)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seedPreContainerSubstrateBaseline: %v\nstatement: %s", err, stmt)
		}
	}
}

// TestAddContainerDesiredState verifies that migration 0045 creates all four
// container-substrate tables on the existing-database upgrade path, that rows
// referencing the pre-seeded plugin/instance parents round-trip, that the
// constraints the reconciler depends on are actually enforced, and that the
// migration is idempotent.
func TestAddContainerDesiredState(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// Build the pre-0045 schema by hand — do NOT call applyInitialSchema.
	seedPreContainerSubstrateBaseline(t, db)

	m := &migrations.AddContainerDesiredState{}

	skip, err := m.ShouldSkip(ctx, db)
	if err != nil {
		t.Fatalf("ShouldSkip: %v", err)
	}
	if skip {
		t.Fatal("ShouldSkip returned true against the pre-target baseline — the hand-crafted DDL must omit the container tables")
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, created_at, updated_at)
		 VALUES ('pl1', 'slack', '1.0.0', '{}', 'pubkey', 'active', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO plugin_instances(id, plugin_id, instance_name, health_state, created_at, updated_at)
		 VALUES ('inst1', 'pl1', 'prod', 'healthy', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed plugin instance: %v", err)
	}

	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var tableCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN
			('plugin_containers', 'plugin_container_generations', 'plugin_container_subnets', 'plugin_container_images')`,
	).Scan(&tableCount); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if tableCount != 4 {
		t.Fatalf("table count = %d, want 4", tableCount)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO plugin_containers
			(id, plugin_instance_id, image_ref, image_digest, config_hash, network_name, desired_state, created_at, updated_at)
		 VALUES
			('c1', 'inst1', 'gleipnir/slack:1.0.0', 'sha256:aaaa', 'cfg', 'net-inst1', 'running', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin_containers row: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO plugin_container_generations
			(id, plugin_instance_id, generation, image_digest, config_hash, token_hash, status, created_at, updated_at)
		 VALUES
			('g1', 'inst1', 1, 'sha256:aaaa', 'cfg', 'tokenhash', 'active', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin_container_generations row: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO plugin_container_subnets (subnet, plugin_instance_id, pool_base, slot, allocated_at)
		 VALUES ('10.83.0.0/24', 'inst1', '10.83.0.0/16', 0, '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin_container_subnets row: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO plugin_container_images (digest, reference, plugin_id, loaded_at)
		 VALUES ('sha256:aaaa', 'gleipnir/slack:1.0.0', 'pl1', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin_container_images row: %v", err)
	}

	// The constraints the reconciler relies on must survive the upgrade path,
	// not just the fresh-install one: a second allocation of one slot is what
	// would silently put two instances on the same network.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO plugin_container_subnets (subnet, plugin_instance_id, pool_base, slot, allocated_at)
		 VALUES ('10.83.99.0/24', 'inst1', '10.83.0.0/16', 0, '2024-01-01T00:00:00Z')`,
	); err == nil {
		t.Error("a duplicate (pool_base, slot) allocation was accepted after migration, want a UNIQUE violation")
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO plugin_container_generations
			(id, plugin_instance_id, generation, image_digest, config_hash, token_hash, status, created_at, updated_at)
		 VALUES
			('g2', 'inst1', 2, 'sha256:aaaa', 'cfg', 'tokenhash', 'pending', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err == nil {
		t.Error("a duplicate token hash was accepted after migration, want a UNIQUE violation")
	}

	// Second Apply must be a no-op — proves ShouldSkip flips to true after Up.
	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("second Apply (idempotency): %v", err)
	}
}

// TestAddContainerDesiredStateSkipsOnFreshSchema is the regression gate for
// forgetting to hand-sync 0001_initial.sql: sqlc.yaml reads only that file, so
// a Go migration added without editing it would leave sqlc unable to see the
// tables at all.
func TestAddContainerDesiredStateSkipsOnFreshSchema(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	applyInitialSchema(t, db)

	skip, err := (&migrations.AddContainerDesiredState{}).ShouldSkip(ctx, db)
	if err != nil {
		t.Fatalf("ShouldSkip: %v", err)
	}
	if !skip {
		t.Fatal("ShouldSkip returned false on a fresh initial schema — did 0001_initial.sql forget the container substrate tables?")
	}
}

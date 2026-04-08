// Package db manages the SQLite connection lifecycle and schema migrations.
// Query access is provided by the sqlc-generated Queries type (run
// `sqlc generate` to produce internal/db/*.sql.go from internal/db/queries/).
package db

//go:generate sqlc generate -f ../../sqlc.yaml

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "modernc.org/sqlite" // register the sqlite driver

	"github.com/rapp992/gleipnir/internal/model"
)

//go:embed migrations/0001_initial.sql
var initialSchema string

// queries wraps the sqlc-generated Queries so the embedding is unexported.
type queries struct{ *Queries }

// Store wraps the database connection and provides lifecycle methods.
type Store struct {
	db *sql.DB
	queries
}

// Open opens the SQLite database at path, enables WAL mode and foreign keys,
// and returns a ready Store.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Single open connection: SQLite serializes writes at the file level, so
	// multiple connections contend for the write lock and produce SQLITE_BUSY
	// errors. One connection avoids that without needing a busy_timeout retry
	// loop — go's database/sql serializes callers via the pool.
	db.SetMaxOpenConns(1)

	// WAL mode for concurrent reads alongside serialized audit writes (ADR-003).
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}

	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	return &Store{db: db, queries: queries{New(db)}}, nil
}

// DB returns the underlying *sql.DB.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Queries returns the sqlc-generated query object for callers that need to
// pass it to components outside the Store (e.g. agent.AuditWriter).
func (s *Store) Queries() *Queries { return s.queries.Queries }

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Migrate applies the embedded initial schema if it has not been applied.
// It is idempotent — safe to call on every startup.
func (s *Store) Migrate(ctx context.Context) error {
	// Check whether schema_migrations exists. If it doesn't, the schema has
	// never been applied and we run the full DDL.
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("check schema_migrations: %w", err)
	}

	if count == 0 {
		// Wrap in a transaction so a mid-DDL failure leaves a clean DB.
		// SQLite DDL is transactional, so a rollback undoes all CREATE TABLE
		// and INSERT statements, allowing the next startup to retry cleanly.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration tx: %w", err)
		}
		defer func() {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				slog.Error("migration transaction rollback failed", "err", rbErr)
			}
		}()
		if _, err := tx.ExecContext(ctx, initialSchema); err != nil {
			return fmt.Errorf("apply initial schema: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration tx: %w", err)
		}
		return nil
	}

	// Table exists — verify version 1 was recorded.
	var applied int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 1`,
	).Scan(&applied)
	if err != nil {
		return fmt.Errorf("check migration version: %w", err)
	}
	if applied == 0 {
		return fmt.Errorf("schema_migrations exists but version 1 is not recorded")
	}

	if err := s.migrateAddThinkingStepType(ctx); err != nil {
		return fmt.Errorf("migrate thinking step type: %w", err)
	}

	if err := s.migrateAddTriggerQueue(ctx); err != nil {
		return fmt.Errorf("migrate trigger queue: %w", err)
	}

	if err := s.migrateAddWaitingForFeedback(ctx); err != nil {
		return fmt.Errorf("migrate waiting_for_feedback status: %w", err)
	}

	if err := s.migrateAddFeedbackRequests(ctx); err != nil {
		return fmt.Errorf("migrate feedback_requests table: %w", err)
	}

	if err := s.migrateDropCapabilityRole(ctx); err != nil {
		return fmt.Errorf("migrate drop capability_role: %w", err)
	}

	if err := s.migrateAddFeedbackExpiresAt(ctx); err != nil {
		return fmt.Errorf("migrate add feedback expires_at: %w", err)
	}

	if err := s.migrateAddRunModel(ctx); err != nil {
		return fmt.Errorf("migrate add run model: %w", err)
	}

	if err := s.migrateAddUserPreferencesAndSessionUA(ctx); err != nil {
		return fmt.Errorf("migrate add user preferences and session ua: %w", err)
	}

	if err := s.migrateAddPollTriggerType(ctx); err != nil {
		return fmt.Errorf("migrate add poll trigger type: %w", err)
	}

	if err := s.migrateAddSystemAndModelSettings(ctx); err != nil {
		return fmt.Errorf("migrate add system and model settings: %w", err)
	}

	if err := s.migrateAddOpenAICompatProviders(ctx); err != nil {
		return fmt.Errorf("migrate add openai_compat_providers: %w", err)
	}

	return nil
}

// migrateAddOpenAICompatProviders creates the openai_compat_providers table
// on existing deployments. New deployments get it from 0001_initial.sql; this
// migration is a no-op for them.
func (s *Store) migrateAddOpenAICompatProviders(ctx context.Context) error {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='openai_compat_providers'`,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("check openai_compat_providers existence: %w", err)
	}
	if count > 0 {
		return nil
	}

	ddl := `
CREATE TABLE openai_compat_providers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL UNIQUE,
    base_url          TEXT    NOT NULL,
    api_key_encrypted TEXT    NOT NULL,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);

CREATE INDEX idx_openai_compat_providers_name ON openai_compat_providers(name);`

	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create openai_compat_providers: %w", err)
	}

	slog.Info("migrated: created openai_compat_providers table")
	return nil
}

// migrateAddThinkingStepType updates the run_steps CHECK constraint to include
// 'thinking' on existing deployments where 0001_initial.sql was applied before
// this value existed. New deployments get it directly from 0001_initial.sql.
//
// SQLite does not support ALTER COLUMN to modify CHECK constraints, so we use
// the table-recreation pattern: create a new table, copy data, drop old, rename.
func (s *Store) migrateAddThinkingStepType(ctx context.Context) error {
	var tableSQL string
	err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='run_steps'`,
	).Scan(&tableSQL)
	if err != nil {
		return fmt.Errorf("query run_steps schema: %w", err)
	}

	if strings.Contains(tableSQL, "'thinking'") {
		return nil // already present; nothing to do
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin thinking migration tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("thinking migration rollback failed", "err", rbErr)
		}
	}()

	ddl := `
CREATE TABLE run_steps_new (
    id          TEXT    PRIMARY KEY,
    run_id      TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_number INTEGER NOT NULL,
    type        TEXT    NOT NULL CHECK(type IN (
                    'capability_snapshot',
                    'thought',
                    'thinking',
                    'tool_call',
                    'tool_result',
                    'approval_request',
                    'feedback_request',
                    'feedback_response',
                    'error',
                    'complete'
                )),
    content     TEXT    NOT NULL,
    token_cost  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL,
    UNIQUE(run_id, step_number)
);
INSERT INTO run_steps_new SELECT * FROM run_steps;
DROP TABLE run_steps;
ALTER TABLE run_steps_new RENAME TO run_steps;
CREATE INDEX idx_run_steps_run_step ON run_steps(run_id, step_number);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("recreate run_steps with thinking: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit thinking migration: %w", err)
	}

	slog.Info("migrated run_steps table to include thinking step type")
	return nil
}

// migrateAddTriggerQueue creates the trigger_queue table and its index on
// existing deployments that were initialized before this table was added.
// New deployments get it from 0001_initial.sql; this migration is a no-op for them.
func (s *Store) migrateAddTriggerQueue(ctx context.Context) error {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='trigger_queue'`,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("check trigger_queue existence: %w", err)
	}
	if count > 0 {
		return nil // already present; nothing to do
	}

	ddl := `
CREATE TABLE trigger_queue (
    id              TEXT    PRIMARY KEY,
    policy_id       TEXT    NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled')),
    trigger_payload TEXT    NOT NULL,
    position        INTEGER NOT NULL,
    created_at      TEXT    NOT NULL,
    UNIQUE(policy_id, position)
);
CREATE INDEX idx_trigger_queue_policy_position ON trigger_queue(policy_id, position);`

	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create trigger_queue: %w", err)
	}

	slog.Info("migrated: created trigger_queue table")
	return nil
}

// migrateAddWaitingForFeedback updates the runs table CHECK constraint to include
// 'waiting_for_feedback' on existing deployments. New deployments get it from
// 0001_initial.sql directly.
//
// SQLite does not support ALTER COLUMN, so we use the table-recreation pattern.
// The PRAGMA foreign_keys change must be executed OUTSIDE the transaction because
// SQLite does not permit changing it inside a transaction.
func (s *Store) migrateAddWaitingForFeedback(ctx context.Context) error {
	var tableSQL string
	err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='runs'`,
	).Scan(&tableSQL)
	if err != nil {
		return fmt.Errorf("query runs schema: %w", err)
	}

	if strings.Contains(tableSQL, "'waiting_for_feedback'") {
		return nil // already present; nothing to do
	}

	// Disable FK enforcement before the transaction so that the DROP TABLE
	// inside the transaction does not trigger FK violations from child tables
	// (run_steps, approval_requests, feedback_requests). Re-enable after commit.
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys before runs migration: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("begin waiting_for_feedback migration tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("waiting_for_feedback migration rollback failed", "err", rbErr)
		}
	}()

	ddl := `
CREATE TABLE runs_new (
    id              TEXT    PRIMARY KEY,
    policy_id       TEXT    NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    status          TEXT    NOT NULL CHECK(status IN (
                        'pending',
                        'running',
                        'waiting_for_approval',
                        'waiting_for_feedback',
                        'complete',
                        'failed',
                        'interrupted'
                    )),
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled')),
    trigger_payload TEXT    NOT NULL,
    started_at      TEXT    NOT NULL,
    completed_at    TEXT,
    token_cost      INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    thread_id       TEXT,
    created_at      TEXT    NOT NULL,
    system_prompt   TEXT
);
INSERT INTO runs_new SELECT * FROM runs;
DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;
CREATE INDEX idx_runs_status         ON runs(status);
CREATE INDEX idx_runs_created_at     ON runs(created_at DESC);
CREATE INDEX idx_runs_policy_created ON runs(policy_id, created_at DESC);
CREATE INDEX idx_runs_policy_status  ON runs(policy_id, status);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("recreate runs table with waiting_for_feedback: %w", err)
	}
	if err := tx.Commit(); err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("commit waiting_for_feedback migration: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("re-enable foreign keys after runs migration: %w", err)
	}

	slog.Info("migrated runs table to include waiting_for_feedback status")
	return nil
}

// migrateAddFeedbackRequests creates the feedback_requests table and its indexes on
// existing deployments. New deployments get it from 0001_initial.sql; this migration
// is a no-op for them.
func (s *Store) migrateAddFeedbackRequests(ctx context.Context) error {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='feedback_requests'`,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("check feedback_requests existence: %w", err)
	}
	if count > 0 {
		return nil // already present; nothing to do
	}

	ddl := `
CREATE TABLE feedback_requests (
    id              TEXT    PRIMARY KEY,
    run_id          TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    tool_name       TEXT    NOT NULL,
    proposed_input  TEXT    NOT NULL,
    message         TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK(status IN ('pending', 'resolved')),
    response        TEXT,
    resolved_at     TEXT,
    created_at      TEXT    NOT NULL
);
CREATE INDEX idx_feedback_requests_run_id         ON feedback_requests(run_id);
CREATE INDEX idx_feedback_requests_status         ON feedback_requests(status);
CREATE INDEX idx_feedback_requests_run_pending    ON feedback_requests(run_id, status);`

	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create feedback_requests: %w", err)
	}

	slog.Info("migrated: created feedback_requests table")
	return nil
}

// migrateDropCapabilityRole drops the capability_role column from mcp_tools on
// existing deployments where it was present. New deployments get the schema
// without it from 0001_initial.sql. The tool/feedback distinction is now handled
// by the Gleipnir runtime as a native feedback primitive (ADR-031).
//
// SQLite does not support DROP COLUMN in older versions, so we use the
// table-recreation pattern. PRAGMA foreign_keys must be set OUTSIDE the
// transaction (SQLite requirement — see migrateAddWaitingForFeedback pattern).
func (s *Store) migrateDropCapabilityRole(ctx context.Context) error {
	var tableSQL string
	err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='mcp_tools'`,
	).Scan(&tableSQL)
	if err != nil {
		return fmt.Errorf("query mcp_tools schema: %w", err)
	}

	if !strings.Contains(tableSQL, "capability_role") {
		return nil // already dropped; nothing to do
	}

	// Disable FK enforcement before the transaction so that DROP TABLE inside
	// the transaction does not trigger FK violations from child tables.
	// mcp_tools itself has an FK to mcp_servers; we need FK off during the drop.
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys before mcp_tools migration: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("begin drop capability_role migration tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("drop capability_role migration rollback failed", "err", rbErr)
		}
	}()

	ddl := `
CREATE TABLE mcp_tools_new (
    id              TEXT    PRIMARY KEY,
    server_id       TEXT    NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    description     TEXT    NOT NULL,
    input_schema    TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,
    UNIQUE(server_id, name)
);
INSERT INTO mcp_tools_new (id, server_id, name, description, input_schema, created_at)
    SELECT id, server_id, name, description, input_schema, created_at FROM mcp_tools;
DROP TABLE mcp_tools;
ALTER TABLE mcp_tools_new RENAME TO mcp_tools;
CREATE INDEX idx_mcp_tools_server_id ON mcp_tools(server_id);
INSERT INTO schema_migrations(version, applied_at) VALUES (10, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("recreate mcp_tools without capability_role: %w", err)
	}
	if err := tx.Commit(); err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("commit drop capability_role migration: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("re-enable foreign keys after mcp_tools migration: %w", err)
	}

	slog.Info("migrated mcp_tools table to drop capability_role")
	return nil
}

// migrateAddFeedbackExpiresAt adds the expires_at column to feedback_requests and
// updates the status CHECK constraint to include 'timed_out'. New deployments get
// both from 0001_initial.sql; this migration is a no-op for them.
//
// SQLite does not support ALTER COLUMN, so we use the table-recreation pattern.
// PRAGMA foreign_keys must be set OUTSIDE the transaction (SQLite requirement —
// see migrateAddWaitingForFeedback for the same pattern).
func (s *Store) migrateAddFeedbackExpiresAt(ctx context.Context) error {
	// Check whether expires_at already exists by inspecting the column list.
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(feedback_requests)")
	if err != nil {
		return fmt.Errorf("pragma table_info feedback_requests: %w", err)
	}
	defer rows.Close()

	var hasExpiresAt bool
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "expires_at" {
			hasExpiresAt = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pragma rows: %w", err)
	}
	if hasExpiresAt {
		return nil // already present; nothing to do
	}

	// Disable FK enforcement before the transaction so DROP TABLE inside the
	// transaction does not trigger FK violations from run_steps (which reference runs).
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys before feedback_requests migration: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("begin feedback expires_at migration tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("feedback expires_at migration rollback failed", "err", rbErr)
		}
	}()

	ddl := `
CREATE TABLE feedback_requests_new (
    id              TEXT    PRIMARY KEY,
    run_id          TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    tool_name       TEXT    NOT NULL,
    proposed_input  TEXT    NOT NULL,
    message         TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK(status IN ('pending', 'resolved', 'timed_out')),
    response        TEXT,
    resolved_at     TEXT,
    expires_at      TEXT,
    created_at      TEXT    NOT NULL
);
INSERT INTO feedback_requests_new (id, run_id, tool_name, proposed_input, message, status, response, resolved_at, expires_at, created_at)
    SELECT id, run_id, tool_name, proposed_input, message, status, response, resolved_at, NULL, created_at
    FROM feedback_requests;
DROP TABLE feedback_requests;
ALTER TABLE feedback_requests_new RENAME TO feedback_requests;
CREATE INDEX idx_feedback_requests_run_id         ON feedback_requests(run_id);
CREATE INDEX idx_feedback_requests_status         ON feedback_requests(status);
CREATE INDEX idx_feedback_requests_run_pending    ON feedback_requests(run_id, status);
CREATE INDEX idx_feedback_requests_status_expires ON feedback_requests(status, expires_at);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("recreate feedback_requests with expires_at: %w", err)
	}
	if err := tx.Commit(); err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("commit feedback expires_at migration: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("re-enable foreign keys after feedback_requests migration: %w", err)
	}

	slog.Info("migrated feedback_requests table to add expires_at and timed_out status")
	return nil
}

// migrateAddRunModel adds the model column to the runs table on existing deployments.
// New deployments get it from 0001_initial.sql; this migration is a no-op for them.
// SQLite supports ALTER TABLE ADD COLUMN for simple NOT NULL DEFAULT columns without
// needing table recreation.
func (s *Store) migrateAddRunModel(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(runs)")
	if err != nil {
		return fmt.Errorf("pragma table_info runs: %w", err)
	}
	defer rows.Close()

	var hasModel bool
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "model" {
			hasModel = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pragma rows: %w", err)
	}
	if hasModel {
		return nil // already present; nothing to do
	}

	if _, err := s.db.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN model TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("alter table runs add model: %w", err)
	}

	slog.Info("migrated runs table to add model column")
	return nil
}

// migrateAddUserPreferencesAndSessionUA creates the user_preferences table and adds
// user_agent/ip_address columns to sessions on existing deployments.
// New deployments get both from 0001_initial.sql; this migration is a no-op for them.
// SQLite supports ALTER TABLE ADD COLUMN for simple NOT NULL DEFAULT columns without
// needing table recreation.
func (s *Store) migrateAddUserPreferencesAndSessionUA(ctx context.Context) error {
	// Check whether user_preferences table exists.
	var prefCount int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user_preferences'`,
	).Scan(&prefCount)
	if err != nil {
		return fmt.Errorf("check user_preferences existence: %w", err)
	}
	if prefCount == 0 {
		ddl := `CREATE TABLE user_preferences (
    user_id          TEXT NOT NULL REFERENCES users(id),
    preference_key   TEXT NOT NULL,
    preference_value TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    UNIQUE(user_id, preference_key)
);`
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create user_preferences: %w", err)
		}
		slog.Info("migrated: created user_preferences table")
	}

	// Add user_agent column to sessions if it doesn't exist.
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(sessions)")
	if err != nil {
		return fmt.Errorf("pragma table_info sessions: %w", err)
	}
	defer rows.Close()

	var hasUserAgent, hasIPAddress bool
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "user_agent" {
			hasUserAgent = true
		}
		if name == "ip_address" {
			hasIPAddress = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pragma rows: %w", err)
	}

	if !hasUserAgent {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("alter table sessions add user_agent: %w", err)
		}
		slog.Info("migrated sessions table to add user_agent column")
	}
	if !hasIPAddress {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN ip_address TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("alter table sessions add ip_address: %w", err)
		}
		slog.Info("migrated sessions table to add ip_address column")
	}

	return nil
}

// migrateAddPollTriggerType updates the CHECK constraints on policies, runs,
// and trigger_queue to include 'poll', and creates the poll_states table.
// New deployments get all of this from 0001_initial.sql; this migration is a
// no-op for them.
//
// SQLite cannot ALTER CHECK constraints, so we use the table-recreation pattern
// established by migrateAddWaitingForFeedback. PRAGMA foreign_keys must be
// toggled OUTSIDE the transaction (SQLite requirement).
func (s *Store) migrateAddPollTriggerType(ctx context.Context) error {
	// Check whether 'poll' is already present in the policies DDL. If so, all
	// three tables and the poll_states table have already been migrated.
	var policiesSQL string
	err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='policies'`,
	).Scan(&policiesSQL)
	if err != nil {
		return fmt.Errorf("query policies schema: %w", err)
	}
	if strings.Contains(policiesSQL, "'poll'") {
		return nil // already migrated; nothing to do
	}

	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys before poll migration: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("begin poll migration tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("poll migration rollback failed", "err", rbErr)
		}
	}()

	ddl := `
CREATE TABLE policies_new (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL UNIQUE,
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll')),
    yaml            TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    paused_at       TEXT
);
INSERT INTO policies_new SELECT * FROM policies;
DROP TABLE policies;
ALTER TABLE policies_new RENAME TO policies;
CREATE INDEX idx_policies_trigger_type ON policies(trigger_type);

CREATE TABLE runs_new (
    id              TEXT    PRIMARY KEY,
    policy_id       TEXT    NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    status          TEXT    NOT NULL CHECK(status IN (
                        'pending',
                        'running',
                        'waiting_for_approval',
                        'waiting_for_feedback',
                        'complete',
                        'failed',
                        'interrupted'
                    )),
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll')),
    trigger_payload TEXT    NOT NULL,
    started_at      TEXT    NOT NULL,
    completed_at    TEXT,
    token_cost      INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    thread_id       TEXT,
    created_at      TEXT    NOT NULL,
    system_prompt   TEXT,
    model           TEXT    NOT NULL DEFAULT ''
);
INSERT INTO runs_new SELECT * FROM runs;
DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;
CREATE INDEX idx_runs_status         ON runs(status);
CREATE INDEX idx_runs_created_at     ON runs(created_at DESC);
CREATE INDEX idx_runs_policy_created ON runs(policy_id, created_at DESC);
CREATE INDEX idx_runs_policy_status  ON runs(policy_id, status);

CREATE TABLE trigger_queue_new (
    id              TEXT    PRIMARY KEY,
    policy_id       TEXT    NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll')),
    trigger_payload TEXT    NOT NULL,
    position        INTEGER NOT NULL,
    created_at      TEXT    NOT NULL,
    UNIQUE(policy_id, position)
);
INSERT INTO trigger_queue_new SELECT * FROM trigger_queue;
DROP TABLE trigger_queue;
ALTER TABLE trigger_queue_new RENAME TO trigger_queue;
CREATE INDEX idx_trigger_queue_policy_position ON trigger_queue(policy_id, position);

CREATE TABLE IF NOT EXISTS poll_states (
    policy_id            TEXT    PRIMARY KEY REFERENCES policies(id) ON DELETE CASCADE,
    last_poll_at         TEXT,
    last_result_hash     TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    next_poll_at         TEXT    NOT NULL,
    created_at           TEXT    NOT NULL,
    updated_at           TEXT    NOT NULL
);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("recreate tables for poll trigger type: %w", err)
	}
	if err := tx.Commit(); err != nil {
		s.reenableForeignKeys(ctx)
		return fmt.Errorf("commit poll migration: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("re-enable foreign keys after poll migration: %w", err)
	}

	slog.Info("migrated tables to include poll trigger type and created poll_states table")
	return nil
}

// migrateAddSystemAndModelSettings creates the system_settings and model_settings
// tables on existing deployments. New deployments get both from 0001_initial.sql;
// this migration is a no-op for them.
func (s *Store) migrateAddSystemAndModelSettings(ctx context.Context) error {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='system_settings'`,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("check system_settings existence: %w", err)
	}
	if count > 0 {
		return nil
	}

	ddl := `
CREATE TABLE system_settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE model_settings (
    provider    TEXT    NOT NULL,
    model_name  TEXT    NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    updated_at  TEXT    NOT NULL,
    PRIMARY KEY (provider, model_name)
);`

	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create system_settings and model_settings: %w", err)
	}

	slog.Info("migrated: created system_settings and model_settings tables")
	return nil
}

// reenableForeignKeys re-enables the SQLite foreign_keys pragma after a
// table-recreation migration disables it. The error is logged rather than
// returned because at this point the migration itself has already succeeded —
// FK enforcement will be restored on the next connection anyway, but we want
// visibility if something unusual goes wrong.
func (s *Store) reenableForeignKeys(ctx context.Context) {
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		slog.Warn("failed to re-enable foreign_keys pragma after migration error", "err", err)
	}
}

// ScanOrphanedRuns finds any runs left in 'running', 'waiting_for_approval', or
// 'waiting_for_feedback' state from a previous process crash, inserts an error
// run_step for each, and marks them 'interrupted'. Called once at startup before
// accepting traffic (ADR-011). Errors for individual runs are logged and skipped
// — startup must not be blocked by a partially-corrupted run.
func (s *Store) ScanOrphanedRuns(ctx context.Context, logger *slog.Logger) error {
	runs, err := s.queries.ListOrphanedRuns(ctx)
	if err != nil {
		return fmt.Errorf("list orphaned runs: %w", err)
	}
	if len(runs) == 0 {
		return nil
	}

	for _, run := range runs {
		if err := s.interruptOrphanedRun(ctx, run.ID); err != nil {
			logger.Error("failed to mark orphaned run as interrupted", "run_id", run.ID, "err", err)
			continue
		}
		logger.Warn("marked orphaned run as interrupted", "run_id", run.ID)
	}
	return nil
}

// interruptOrphanedRun inserts an error step and updates the run to 'interrupted'.
func (s *Store) interruptOrphanedRun(ctx context.Context, runID string) error {
	count, err := s.queries.CountRunSteps(ctx, runID)
	if err != nil {
		return fmt.Errorf("count run steps: %w", err)
	}

	content, err := json.Marshal(map[string]string{
		"message": "run interrupted by process restart",
		"code":    "interrupted",
	})
	if err != nil {
		// Encoding a static string map cannot fail; this branch is unreachable in practice.
		return fmt.Errorf("marshal interrupted run content: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.queries.CreateRunStep(ctx, CreateRunStepParams{
		ID:         model.NewULID(),
		RunID:      runID,
		StepNumber: count,
		Type:       string(model.StepTypeError),
		Content:    string(content),
		TokenCost:  0,
		CreatedAt:  now,
	}); err != nil {
		return fmt.Errorf("create error step: %w", err)
	}

	errMsg := "process restarted while run was active"
	if err := s.queries.UpdateRunError(ctx, UpdateRunErrorParams{
		Status:      string(model.RunStatusInterrupted),
		Error:       &errMsg,
		CompletedAt: &now,
		ID:          runID,
	}); err != nil {
		return fmt.Errorf("update run error: %w", err)
	}
	return nil
}

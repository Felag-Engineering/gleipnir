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

// ScanOrphanedRuns finds any runs left in 'running' or 'waiting_for_approval'
// state from a previous process crash, inserts an error run_step for each,
// and marks them 'interrupted'. Called once at startup before accepting traffic
// (ADR-011). Errors for individual runs are logged and skipped — startup must
// not be blocked by a partially-corrupted run.
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

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
	"fmt"
	"log/slog"
	"time"

	_ "modernc.org/sqlite" // register the sqlite driver

	"github.com/rapp992/gleipnir/internal/model"
)

//go:embed migrations/0001_initial.sql
var initialSchema string

//go:embed migrations/0002_add_manual_trigger.sql
var migration0002 string

//go:embed migrations/0003_add_system_prompt.sql
var migration0003 string

//go:embed migrations/0004_add_scheduled_trigger.sql
var migration0004 string

// Store wraps the database connection and provides lifecycle methods.
type Store struct {
	db *sql.DB
	*Queries
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

	return &Store{db: db, Queries: New(db)}, nil
}

// DB returns the underlying *sql.DB.
func (s *Store) DB() *sql.DB {
	return s.db
}

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
		defer tx.Rollback()
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

	// Apply migration 0002 if not yet recorded.
	var v2Applied int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 2`,
	).Scan(&v2Applied)
	if err != nil {
		return fmt.Errorf("check migration 2: %w", err)
	}
	if v2Applied == 0 {
		if _, err := s.db.ExecContext(ctx, migration0002); err != nil {
			return fmt.Errorf("apply migration 0002: %w", err)
		}
	}

	// Apply migration 0003 if not yet recorded.
	var v3Applied int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 3`,
	).Scan(&v3Applied)
	if err != nil {
		return fmt.Errorf("check migration 3: %w", err)
	}
	if v3Applied == 0 {
		if _, err := s.db.ExecContext(ctx, migration0003); err != nil {
			return fmt.Errorf("apply migration 0003: %w", err)
		}
	}

	// Apply migration 0004 if not yet recorded.
	var v4Applied int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 4`,
	).Scan(&v4Applied)
	if err != nil {
		return fmt.Errorf("check migration 4: %w", err)
	}
	if v4Applied == 0 {
		if _, err := s.db.ExecContext(ctx, migration0004); err != nil {
			return fmt.Errorf("apply migration 0004: %w", err)
		}
	}

	return nil
}

// ScanOrphanedRuns finds any runs left in 'running' or 'waiting_for_approval'
// state from a previous process crash, inserts an error run_step for each,
// and marks them 'interrupted'. Called once at startup before accepting traffic
// (ADR-011). Errors for individual runs are logged and skipped — startup must
// not be blocked by a partially-corrupted run.
func (s *Store) ScanOrphanedRuns(ctx context.Context, logger *slog.Logger) error {
	runs, err := s.Queries.ListOrphanedRuns(ctx)
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
	count, err := s.Queries.CountRunSteps(ctx, runID)
	if err != nil {
		return fmt.Errorf("count run steps: %w", err)
	}

	content, _ := json.Marshal(map[string]string{
		"message": "run interrupted by process restart",
		"code":    "interrupted",
	})

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.Queries.CreateRunStep(ctx, CreateRunStepParams{
		ID:         model.NewULID(),
		RunID:      runID,
		StepNumber: count + 1,
		Type:       string(model.StepTypeError),
		Content:    string(content),
		TokenCost:  0,
		CreatedAt:  now,
	}); err != nil {
		return fmt.Errorf("create error step: %w", err)
	}

	errMsg := "process restarted while run was active"
	if err := s.Queries.UpdateRunError(ctx, UpdateRunErrorParams{
		Status:      string(model.RunStatusInterrupted),
		Error:       &errMsg,
		CompletedAt: &now,
		ID:          runID,
	}); err != nil {
		return fmt.Errorf("update run error: %w", err)
	}
	return nil
}

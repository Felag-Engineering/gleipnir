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
	"time"

	_ "modernc.org/sqlite" // register the sqlite driver

	"github.com/rapp992/gleipnir/internal/db/migrations"
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

// Migrate applies the embedded initial schema if it has not been applied, then
// runs all Go migrations in internal/db/migrations.
//
// Go migrations MUST run on both the fresh-DB path and the upgrading-DB path.
// The initial schema (0001_initial.sql) does not contain DDL for tables added
// by later Go migrations (e.g. system_settings, openai_compat_providers). Each
// migration's ShouldSkip method checks sqlite_master, so migrations that are
// already present in the schema are skipped safely — this is what makes calling
// migrations.Apply on a fresh DB a no-op for those migrations.
//
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
		// Fall through to run Go migrations — they add tables not present in
		// the initial schema and use ShouldSkip to no-op when already present.
	} else {
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
	}

	return migrations.Apply(ctx, s.db, migrations.All(), slog.Default())
}

// ScanOrphanedRuns finds any runs left in a non-terminal, non-pending state
// from a previous process crash (i.e. 'running', 'waiting_for_approval', or
// 'waiting_for_feedback' — the active states per model.IsTerminalStatus), inserts
// an error run_step for each, and marks them 'interrupted'. 'pending' is excluded
// because pending → interrupted is not a legal state transition. Called once at
// startup before accepting traffic (ADR-011). Errors for individual runs are
// logged and skipped — startup must not be blocked by a partially-corrupted run.
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

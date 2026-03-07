// Package db manages the SQLite connection lifecycle and schema migrations.
// Query access is provided by the sqlc-generated Queries type (run
// `sqlc generate` to produce internal/db/*.sql.go from internal/db/queries/).
package db

//go:generate sqlc generate -f ../../sqlc.yaml

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // register the sqlite driver
)

//go:embed migrations/0001_initial.sql
var initialSchema string

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
	return nil
}

// MarkInterrupted sets status = 'interrupted' for any run that was in
// 'running' or 'waiting_for_approval' state when the process last exited.
// Called once at startup before accepting traffic (ADR-011).
func (s *Store) MarkInterrupted(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.Queries.MarkInterruptedRuns(ctx, &now); err != nil {
		return fmt.Errorf("mark interrupted runs: %w", err)
	}
	return nil
}

// Package db manages the SQLite connection lifecycle and schema migrations.
// Query access is provided by the sqlc-generated Queries type (run
// `sqlc generate` to produce internal/db/*.sql.go from internal/db/queries/).
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // register the sqlite driver
)

// Store wraps the database connection and provides lifecycle methods.
// After sqlc generate, embed *Queries here for query access.
type Store struct {
	db *sql.DB
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

	return &Store{db: db}, nil
}

// DB returns the underlying *sql.DB, for use by sqlc-generated Queries.
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
	// TODO: embed schemas/sql_schemas.sql using go:embed and apply if
	// schema_migrations is empty or missing.
	panic("not implemented")
}

// MarkInterrupted sets status = 'interrupted' for any run that was in
// 'running' or 'waiting_for_approval' state when the process last exited.
// Called once at startup before accepting traffic (ADR-011).
func (s *Store) MarkInterrupted(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'interrupted', completed_at = ?
		 WHERE status IN ('running', 'waiting_for_approval')`, now)
	if err != nil {
		return fmt.Errorf("mark interrupted runs: %w", err)
	}
	return nil
}

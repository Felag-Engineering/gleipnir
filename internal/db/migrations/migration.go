// Package migrations defines the Migration interface and optional extension
// interfaces used by the runner in runner.go. Each migration lives in its own
// file and is registered in All() (registry.go).
//
// Migrations receive *sql.Tx so they cannot accidentally use sqlc-generated
// queries — raw DDL only, consistent with ADR-003.
package migrations

import (
	"context"
	"database/sql"
)

// Migration is the minimal interface every migration must implement.
type Migration interface {
	// Version returns a local ordering label used in log/error messages.
	// It is NOT persisted to schema_migrations (except for the special-case
	// INSERT inside DropCapabilityRole, which is preserved verbatim).
	Version() int

	// Name returns a short human-readable identifier used in log/error messages.
	Name() string

	// Up applies the migration inside the provided transaction.
	Up(ctx context.Context, tx *sql.Tx) error
}

// ShouldSkipper is an optional interface a Migration may implement to signal
// that the migration has already been applied. The runner calls ShouldSkip
// before toggling PRAGMA foreign_keys or opening a transaction, so the DB
// is never disturbed when no work is needed — matching the behavior of the
// original store.go inline migrations.
type ShouldSkipper interface {
	ShouldSkip(ctx context.Context, db *sql.DB) (bool, error)
}

// ForeignKeyToggler is an optional interface for migrations that use the
// table-recreation pattern (create new table, copy, drop old, rename). SQLite
// requires PRAGMA foreign_keys to be toggled OUTSIDE any transaction, so the
// runner handles the PRAGMA dance when this interface is implemented.
type ForeignKeyToggler interface {
	RequiresForeignKeysOff() bool
}

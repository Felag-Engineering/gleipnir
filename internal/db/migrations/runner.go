package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

// Apply runs each migration in order. For each migration it:
//  1. Calls ShouldSkip (if implemented) — skips without touching FK state when true.
//  2. Toggles PRAGMA foreign_keys=OFF (if ForeignKeyToggler returns true).
//  3. Opens a transaction, calls Up, commits.
//  4. Re-enables PRAGMA foreign_keys=ON.
//
// Each migration's Up() is responsible for its own slog.Info call so that we
// only log when real work is done — the runner does not add a success log.
// Errors are wrapped with the migration version and name.
func Apply(ctx context.Context, db *sql.DB, migrations []Migration, _ *slog.Logger) error {
	for _, m := range migrations {
		if err := applyOne(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(ctx context.Context, db *sql.DB, m Migration) error {
	// Check idempotency before touching FK state or opening a transaction.
	if skipper, ok := m.(ShouldSkipper); ok {
		skip, err := skipper.ShouldSkip(ctx, db)
		if err != nil {
			return fmt.Errorf("migration %d %s: check skip: %w", m.Version(), m.Name(), err)
		}
		if skip {
			return nil
		}
	}

	needsFKOff := false
	if toggler, ok := m.(ForeignKeyToggler); ok {
		needsFKOff = toggler.RequiresForeignKeysOff()
	}

	if needsFKOff {
		if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
			return fmt.Errorf("migration %d %s: disable foreign keys: %w", m.Version(), m.Name(), err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		if needsFKOff {
			reenableForeignKeys(ctx, db, m)
		}
		return fmt.Errorf("migration %d %s: begin tx: %w", m.Version(), m.Name(), err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("migration rollback failed", "version", m.Version(), "name", m.Name(), "err", rbErr)
		}
	}()

	if err := m.Up(ctx, tx); err != nil {
		// Rollback explicitly before re-enabling FK: the pool has MaxOpenConns(1),
		// so db.ExecContext in reenableForeignKeys would deadlock waiting for the
		// connection the open transaction is holding.
		_ = tx.Rollback()
		if needsFKOff {
			reenableForeignKeys(ctx, db, m)
		}
		return fmt.Errorf("migration %d %s: %w", m.Version(), m.Name(), err)
	}

	if err := tx.Commit(); err != nil {
		if needsFKOff {
			reenableForeignKeys(ctx, db, m)
		}
		return fmt.Errorf("migration %d %s: commit: %w", m.Version(), m.Name(), err)
	}

	if needsFKOff {
		if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
			return fmt.Errorf("migration %d %s: re-enable foreign keys: %w", m.Version(), m.Name(), err)
		}
	}

	return nil
}

// reenableForeignKeys attempts to restore FK enforcement after a migration
// error. The error is logged rather than returned because at this point the
// migration itself has already failed — FK enforcement will be restored on the
// next connection anyway, but we want visibility if something unusual goes wrong.
func reenableForeignKeys(ctx context.Context, db *sql.DB, m Migration) {
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		slog.Warn("failed to re-enable foreign_keys pragma after migration error",
			"version", m.Version(), "name", m.Name(), "err", err)
	}
}

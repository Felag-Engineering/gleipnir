# Database Migrations and sqlc Workflow

Gleipnir uses SQLite (WAL mode, single connection) with a hybrid migration strategy and sqlc for type-safe query generation.

## How migrations work

Migrations run automatically on every startup via `Store.Migrate()`. There are two kinds:

1. **The initial SQL migration** (`internal/db/migrations/0001_initial.sql`) — creates all base tables in a single transaction. Runs only on a fresh database.
2. **Go migrations** (`internal/db/migrations/0002_*.go` through `0023_*.go`) — incremental changes for post-launch schema evolution. Each implements the `Migration` interface and has a `ShouldSkip()` method for idempotency.

The runner (`internal/db/migrations/runner.go`) handles ordering, foreign key toggling, and transactions. Migrations are registered in `internal/db/migrations/registry.go`.

## Adding a new table

### 1. Write the Go migration

Create `internal/db/migrations/NNNN_add_your_table.go`:

```go
package migrations

import (
    "context"
    "database/sql"
    "fmt"
    "log/slog"
)

type AddYourTable struct{}

func (m *AddYourTable) Version() int { return NNNN }
func (m *AddYourTable) Name() string { return "add_your_table" }

func (m *AddYourTable) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
    var count int
    err := db.QueryRowContext(ctx,
        `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='your_table'`,
    ).Scan(&count)
    return count > 0, err
}

func (m *AddYourTable) Up(ctx context.Context, tx *sql.Tx) error {
    _, err := tx.ExecContext(ctx, `
        CREATE TABLE your_table (
            id         TEXT PRIMARY KEY,
            name       TEXT NOT NULL,
            created_at TEXT NOT NULL
        )`)
    if err != nil {
        return fmt.Errorf("create your_table: %w", err)
    }
    slog.Info("migrated: created your_table table")
    return nil
}
```

### 2. Register the migration

**File:** `internal/db/migrations/registry.go`

Add `&AddYourTable{}` to the end of the `All()` slice. Order matters — it must be after all existing entries.

### 3. Write queries

Create `internal/db/queries/your_table.sql`:

```sql
-- name: CreateYourTableRow :one
INSERT INTO your_table (id, name, created_at)
VALUES (:id, :name, :created_at)
RETURNING *;

-- name: GetYourTableRow :one
SELECT * FROM your_table WHERE id = :id;

-- name: ListYourTableRows :many
SELECT * FROM your_table ORDER BY created_at DESC;
```

Query annotations: `:one` returns a single row, `:many` returns a slice, `:exec` returns nothing. Use `sqlc.narg('param')` for optional filter parameters.

### 4. Update sqlc config (if SQL migration)

If your migration is a `.sql` file (not just Go), add it to the `schema` list in `sqlc.yaml` so sqlc can type-check queries against it.

### 5. Generate code

```bash
sqlc generate
```

This creates/updates:
- `internal/db/models.go` — adds a struct for your table
- `internal/db/your_table.sql.go` — generated query methods

### 6. Update the schema reference

**File:** `schemas/sql_schemas.sql`

Keep this in sync. It's a documentation file (not used by migrations) that gives developers a single-file view of the full schema.

## Adding a query to an existing table

1. Edit the appropriate file in `internal/db/queries/` (e.g., `runs.sql`)
2. Run `sqlc generate`
3. Use the new method: `store.Queries().YourNewQuery(ctx, params)`

No migration needed — you're adding an accessor, not changing the schema.

## Modifying CHECK constraints

SQLite doesn't support `ALTER TABLE ... ALTER CONSTRAINT`. You need to recreate the table:

1. Implement `RequiresForeignKeysOff() bool` returning `true` on your migration (see the `ForeignKeyToggler` interface)
2. In `Up()`: create new table with updated constraints, copy data, drop old, rename new, recreate indexes

See `internal/db/migrations/0013_add_thinking_step_type.go` for the pattern.

## Key details

- **Single connection:** `MaxOpenConns=1` avoids SQLite write contention. Don't change this.
- **WAL mode:** Enabled at startup in `Store.Open()`. Allows concurrent reads during writes.
- **Foreign keys:** Enforced via `PRAGMA foreign_keys=ON` at startup. Migrations that recreate tables must toggle this off.
- **All types are strings:** sqlc generates plain `string` and `int64` fields from SQLite TEXT/INTEGER columns. Conversion to typed model enums happens in the caller, not in `db`.
- **Generated code is committed:** The `internal/db/*.sql.go` and `models.go` files are checked into git. Run `sqlc generate` locally and commit the results.

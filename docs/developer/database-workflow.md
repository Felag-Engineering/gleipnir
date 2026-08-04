# Database Migrations and sqlc Workflow

Gleipnir uses SQLite (WAL mode, single connection) with a hybrid migration strategy and sqlc for type-safe query generation.

## How migrations work

Migrations run automatically on every startup via `Store.Migrate()`. There are two kinds:

1. **The initial SQL migration** (`internal/db/migrations/0001_initial.sql`) — creates all base tables in a single transaction. Runs only on a fresh database.
2. **Incremental migrations** (`0009`–`0039`, a mix of `.go` and `.sql` files) — post-launch schema evolution. Each implements the `Migration` interface; most also implement the optional `ShouldSkipper` interface (`ShouldSkip()`) for idempotency.

The `NNNN_` filename prefix is a hint only — `Version()` (via the `All()` slice in `registry.go`) is authoritative for apply order. Prefixes may carry a suffix (e.g. `0025b_`) to avoid filename collisions, and a migration's `Version()` need not match its filename number (e.g. `0013_add_thinking_step_type.go` returns `Version() 2`).

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

1. Implement `RequiresForeignKeysOff() bool` returning `true` on your migration (see the `ForeignKeyToggler` interface) whenever the recreated table is referenced by foreign keys
2. In `Up()`: create new table with updated constraints, copy data, drop old, rename new, recreate indexes

See `internal/db/migrations/0021_add_poll_trigger_type.go` or `0026_add_cron_trigger_type.go` for the full recreate-with-FK-off pattern. (`0013_add_thinking_step_type.go` shows the table-recreate shape but omits `RequiresForeignKeysOff` because nothing references `run_steps` by FK at that point.)

## Key details

- **Single connection:** `MaxOpenConns=1` avoids SQLite write contention. Don't change this.
- **WAL mode:** Enabled at startup in `Store.Open()`. Allows concurrent reads during writes.
- **Foreign keys:** Enforced via `PRAGMA foreign_keys=ON` at startup. Migrations that recreate tables must toggle this off.
- **All types are strings:** sqlc generates plain `string` and `int64` fields from SQLite TEXT/INTEGER columns. Conversion to typed model enums happens in the caller, not in `db`.
- **Generated code is committed:** The `internal/db/*.sql.go` and `models.go` files are checked into git. Run `sqlc generate` locally and commit the results.
- **Stick to ASCII inside `internal/db/queries/*.sql`:** sqlc 1.30's parameter rewriter has a byte-vs-rune position bug with multi-byte UTF-8 characters in the same file as a parameterised query. A single em-dash or section sign in a comment will silently corrupt downstream `:param` substitutions. Use plain ASCII (`--`, `sec`) in query files; non-ASCII is fine in `schemas/sql_schemas.sql` and migration files because sqlc only parses the queries directory.

## Plugin system tables (ADR-041, ADR-045, ADR-046)

Three tables back the plugin system; they live alongside the rest of the schema and are queried via the `plugins.sql` and `plugin_audit_events.sql` query files:

- **`plugins`** — one row per installed plugin (binary + manifest pair). Holds the TOFU-pinned `trusted_pubkey`, the `manifest_snapshot` used for hot-reload material-change detection, and the install-flow `status` (`pending_review` / `active` / `removed`). The `version` column is the ADR-038 CAS counter; do not confuse with `plugin_version`, which is the author's SemVer string.
- **`plugin_instances`** — one row per configured deployment. Carries `config_json`, `credentials_encrypted` (write-only via the API per ADR-039 / ADR-034 pattern), `handshake_versions` (per-service versions pinned from the `Handshake/v1` exchange), and the `health_state` enum from ADR-045 §7. Same CAS `version` semantics.
- **`plugin_audit_events`** — operator-only audit trail (ADR-046). Append-only, INTEGER primary key. Foreign keys to `plugin_instances` and `users` are nullable with `ON DELETE SET NULL` so audit history outlives uninstalls and user deletions. **Never surfaced to the LLM.**

## Plugin audience tables (spec §6.1, §4.2, §11.5)

Three additional tables support the audience and Channel Request subsystem, added by migration `0030_add_plugin_audiences_and_pending_requests.go` and queried via `plugin_audiences.sql` and `plugin_pending_requests.sql`:

- **`plugin_audiences`** — first-class shared resources (spec §6.1). Each audience is a named ordered list of plugin-instance entries. The `version` column is the ADR-038 CAS counter for atomic edits.

- **`audience_entries`** — ordered member list for an audience. Each row references a `plugin_instances` row. Key FK semantics:
  - `audience_id` → `plugin_audiences(id) ON DELETE CASCADE` — deleting an audience removes all its entries.
  - `plugin_instance_id` → `plugin_instances(id) ON DELETE RESTRICT` — uninstalling an instance is blocked while any audience entry references it (spec §11.5 uninstall gate).
  - The `UNIQUE(audience_id, position)` constraint is **not deferrable** — `modernc.org/sqlite` rejects `DEFERRABLE INITIALLY DEFERRED` on table constraints. Multi-row position swaps inside a single transaction must use a temporary sentinel position to avoid transient violations (e.g. move entry A to a large out-of-range value, move entry B to A's old position, then move A to B's old position).

- **`plugin_pending_requests`** — tracks ChannelService.Request rows once pre-ack has succeeded (spec §4.2). Status enum (`pending` / `resolved` / `timed_out`) mirrors `feedback_requests` so `internal/timeout/scanner.go` can drive both tables with the same `(status, expires_at)` index. Key FK semantics:
  - `run_id` → `runs(id) ON DELETE CASCADE` — deleting a run removes its pending requests.
  - `plugin_instance_id` → `plugin_instances(id) ON DELETE RESTRICT` — matches the §11.5 uninstall gate.
  - `audience_entry_id` → `audience_entries(id) ON DELETE SET NULL` — in-flight Channel Requests continue to resolve after an entry is edited or deleted (spec §11.7 late-callback semantics).
  - The `tool_name TEXT NOT NULL DEFAULT ''` column mirrors `feedback_requests.tool_name` so the timeout scanner has the field it reads when building `ExpiredItem` values.
  - `plugin_pending_requests` is deliberately **not merged** into `feedback_requests`. Combining them would force a `kind` discriminator column across every `internal/feedback/` query; the two tables have different pre-states and substrate semantics (native ADR-031 feedback vs. Channel Request).

## Tool-initiated HITL and MCP task tables (ADR-055, mcp-realignment-spec.md §6)

Two tables added by migration `0044_add_tool_input_requests_and_mcp_tasks.go` and queried via `tool_input_requests.sql` and `mcp_tasks.sql`. Both are schema-only in this slice of the work — nothing yet writes to or polls them; see the spec for the not-yet-implemented consumer (the plugin/container/HITL half of the MCP realignment).

- **`tool_input_requests`** — a durable record of a tool-initiated human-in-the-loop wait: an MCP server paused a `tools/call` with an MRTR `input_required` result (spec §6). Holds the original call (`server_id`, `tool_name`, `call_args`) so it can be retried, the opaque `request_state` the server returned (size-capped at write as defense in depth per §6.2 — enforced by the future caller, not a DB constraint), the elicitation-shaped `request_payload`, the `elicitation_kind` split from §6.1 (`permission` vs `information`), and the effective `expires_at` deadline computed per the §6.3 timeout precedence. `run_id` and `server_id` are both `ON DELETE CASCADE` — deleting the run or the server the call went to moots the wait.
- **`mcp_tasks`** — MCP Tasks-extension handles (SEP-2663) so polling resumes after a restart (the §13 durability claim). `kind` (`tool_call` / `channel_request`) deliberately covers both a task backing a `tool_input_requests` row and the not-yet-wired §6.4 Channel-Request-as-task path — the table is designed for that reuse, not just tool-initiated waits. `status` distinguishes non-terminal (`working`, `input_required`) from terminal (`complete`, `failed`, `cancelled`, `expired`); `expired` is kept separate from `failed` because a server-side TTL lapsing is "weather" (§6.3), not something the server itself reported as a failure. `UNIQUE(server_id, task_id)` reflects that task ids are scoped to the server namespace.

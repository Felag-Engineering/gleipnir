# Adding a New Trigger Type

Gleipnir has four trigger types: `webhook`, `manual`, `scheduled`, `poll`. They all converge on the same `RunLauncher.Launch()` path, but each has its own handler, validation rules, and (optionally) background processing.

This guide walks through every file you need to touch. Use the `poll` trigger (the most recent addition) as your reference implementation.

## Checklist

### 1. Add the enum value

**File:** `internal/model/model.go`

Add a new `TriggerType` constant alongside the existing ones and update the `Valid()` method's switch statement.

### 2. Add trigger-specific config fields (if any)

**File:** `internal/policy/model.go`

The `TriggerConfig` struct holds type-specific fields like `FireAt` (scheduled), `Interval` (poll), and `WebhookSecret` (webhook). Add fields for your new type here.

### 3. Update the policy YAML schema docs

**File:** `schemas/policy.yaml`

Document the new trigger type's required and optional fields following the existing pattern.

### 4. Parse the new fields from YAML

**File:** `internal/policy/parser.go`

Update `convertTrigger()` to handle your new type's YAML fields. If needed, extend the `rawTrigger` struct with intermediate parse types.

### 5. Validate the new trigger type

**File:** `internal/policy/validator.go`

Update `validateTrigger()` with a new case in the switch block. Validate required fields and reject invalid combinations.

### 6. Update database CHECK constraints

**Files:**
- `schemas/sql_schemas.sql` — update CHECK constraints on `policies.trigger_type`, `runs.trigger_type`, and `trigger_queue.trigger_type`
- New migration file in `internal/db/migrations/` — recreate the affected tables with updated CHECK constraints (follow the pattern in `0021_add_poll_trigger_type.go`)
- `internal/db/migrations/registry.go` — register the new migration in `All()`

If your trigger needs persistent state between invocations (like `poll_states`), create the state table in the same migration.

### 7. Add queries (if needed)

**File:** `internal/db/queries/`

If your trigger type has background processing, add a query to fetch active policies of that type (e.g., `GetPollActivePolicies`). If it needs state tracking, add CRUD queries for the state table.

Run `sqlc generate` after adding queries. Update `sqlc.yaml` if you added a new SQL migration file to the schema list.

### 8. Implement the trigger handler

**File:** `internal/trigger/yourtype.go` (new)

Create a handler struct with at minimum `store *db.Store` and `launcher *run.RunLauncher`. The handler either:
- **HTTP-driven** (like webhook/manual): implements `http.HandlerFunc`, validates the request, and calls `launcher.Launch()`
- **Background-driven** (like scheduled/poll): runs a loop or timer, loading policies from DB and calling `launcher.Launch()` when conditions are met

In both cases, construct a `run.LaunchParams` with the correct `TriggerType` and `TriggerPayload`.

### 9. Wire it up in main.go

**File:** `main.go`

- Instantiate your handler (passing `store` and `launcher`)
- If HTTP-driven: add it to the `api.RouterConfig` struct
- If background-driven: call `Start(ctx)` alongside the scheduler and poller, and ensure it respects context cancellation for graceful shutdown

### 10. Register the route (if HTTP-driven)

**File:** `internal/http/api/router.go`

- Add the handler to the `RouterConfig` struct
- Register the route in `BuildRouter()` — external triggers (no auth) go near the webhook handler; internal triggers (auth required) go near the manual trigger handler

### 11. Update the frontend

**Files:**
- `frontend/src/constants/status.ts` — add to the `TriggerType` union and `KNOWN_TRIGGERS` set
- `frontend/src/pages/PolicyEditorPage.tsx` — add UI for trigger-specific configuration fields and the "Run now" button condition if applicable

### 12. Write tests

- Policy parsing: `internal/policy/parser_test.go`
- Policy validation: `internal/policy/validator_test.go`
- Trigger handler: `internal/trigger/yourtype_test.go` (new)
- Migration: verify idempotency in `internal/db/migrations/` tests

## Reference implementations

| Trigger type | Handler file | Background? | Notes |
|-------------|-------------|-------------|-------|
| `webhook` | `internal/trigger/webhook.go` | No | HMAC signature validation, rate limited |
| `manual` | `internal/trigger/manual.go` | No | Simplest — good starting point |
| `scheduled` | `internal/trigger/scheduled.go` | Yes | Loads fire_at times, arms timers |
| `poll` | `internal/trigger/poll.go` | Yes | Cron expression, MCP tool check, hash dedup |

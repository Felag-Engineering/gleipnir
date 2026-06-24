# Adding a New Trigger Type

Gleipnir has six trigger types: `webhook`, `manual`, `scheduled`, `poll`, `cron`, and `subscribed` (the internal-only plugin-sourced type — see the section at the end). They all converge on the same `RunLauncher.LaunchWithConcurrency()` seam, but each has its own handler, validation rules, and (optionally) background processing.

This guide walks through every file you need to touch. Use the `cron` trigger (a recent background-driven addition) or `poll` as your reference implementation.

## Checklist

### 1. Add the enum value

**File:** `internal/model/model.go`

Add a new `TriggerType` constant alongside the existing ones and update the `Valid()` method's switch statement.

### 2. Add trigger-specific config fields (if any)

**File:** `internal/model/model.go`

The `TriggerConfig` struct holds type-specific fields like `FireAt` (scheduled), `Interval` (poll), and `CronExpr` (cron). Add fields for your new type here.

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
- **HTTP-driven** (like webhook/manual): implements `http.HandlerFunc`, validates the request, and calls `writeLaunchOutcome()` (the shared HTTP adapter in `launch.go`)
- **Background-driven** (like scheduled/poll): runs a loop or timer, loading policies from DB and calling `launcher.LaunchWithConcurrency()` when conditions are met

In both cases, construct a `run.LaunchParams` with the correct `TriggerType`, `TriggerPayload`, and `ParsedPolicy`. The concurrency policy and queue depth are read from `ParsedPolicy.Agent` inside `LaunchWithConcurrency` — do not add them to `LaunchParams`.

**HTTP-driven handlers** call `writeLaunchOutcome(ctx, w, h.launcher, params, "your-trigger-type")`. The function writes the response in all cases (202 Launched, 202 Queued, 409 Skipped, 429 QueueFull, 500 errors) — no additional switch is needed.

**Background-driven handlers** call `launcher.LaunchWithConcurrency(ctx, params)` and switch on the result:

```go
res, err := launcher.LaunchWithConcurrency(ctx, params)
if err != nil {
    switch {
    case errors.Is(err, run.ErrConcurrencyQueueFull):
        slog.Warn("yourtype: trigger queue is full", "policy_id", policyID)
    case errors.Is(err, run.ErrConcurrencyUnrecognised):
        slog.Error("yourtype: concurrency check failed", "policy_id", policyID, "err", err)
    case run.IsConcurrencyCheckError(err):
        slog.Error("yourtype: concurrency check failed", "policy_id", policyID, "err", err)
    case run.IsEnqueueError(err):
        slog.Error("yourtype: failed to enqueue trigger", "policy_id", policyID, "err", err)
    default:
        slog.Error("yourtype: failed to launch run", "policy_id", policyID, "run_id", res.RunID, "err", err)
    }
    return
}
switch res.Outcome {
case run.OutcomeLaunched:
    slog.Info("yourtype: run launched", "policy_id", policyID, "run_id", res.RunID)
case run.OutcomeQueued:
    slog.Info("yourtype: trigger queued (active run exists)", "policy_id", policyID)
case run.OutcomeSkipped:
    slog.Info("yourtype: skipping run, active run exists (concurrency: skip)", "policy_id", policyID)
}
```

`ErrConcurrencyQueueFull` and `ErrConcurrencyUnrecognised` are returned verbatim so `errors.Is` works. `run.IsConcurrencyCheckError` and `run.IsEnqueueError` classify the two wrapped DB-error classes (`concurrencyCheckError` / `enqueueError`) that are unexported from the `run` package.

### 9. Wire it up in main.go

**File:** `main.go`

- Instantiate your handler (passing `store` and `launcher`)
- If HTTP-driven: add it to the `api.RouterConfig` struct
- If background-driven: call `Start(ctx)` alongside the scheduler, poller, and cron runner, and ensure it respects context cancellation for graceful shutdown (it must also drain within `GLEIPNIR_DRAIN_TIMEOUT`)

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
| `webhook` | `internal/trigger/webhook.go` | No | HMAC/bearer signature validation, rate limited |
| `manual` | `internal/trigger/manual.go` | No | Simplest — good starting point |
| `scheduled` | `internal/trigger/scheduled.go` | Yes | Loads fire_at times, arms per-timestamp timers, auto-pauses when exhausted |
| `poll` | `internal/trigger/poll.go` | Yes | Recurring interval, MCP tool check, JSONPath condition, hash dedup |
| `cron` | `internal/trigger/cron.go` | Yes | 5-field POSIX cron expression, runs indefinitely |
| `subscribed` | `internal/plugin/trigger/dispatcher.go` | Yes | Plugin-sourced events; dispatched from the hostsvc trigger sink, not `internal/trigger/` (see below) |

## Plugin-sourced (`subscribed`) triggers

Plugin-sourced triggers use a single internal trigger type — `subscribed` — that binds a policy to a `(plugin_instance, event_kind)` pair declared by an installed plugin's manifest. All plugin event_kinds share this one enum value; there is no separate trigger type per plugin or per event_kind.

Operators do not see the word "subscribed" in the UI. The trigger picker is flat and lists every plugin event_kind alongside the five built-in trigger types as peers. Multiple instances of the same plugin contribute disambiguated entries (e.g. `Slack (slack-prod): Channel message` vs `Slack (slack-personal): Channel message`).

Per-binding filters are typed form fields derived from the manifest's `event_kinds[].binding_schema` (JSON Schema). JSONPath is not used for plugin trigger bindings — bindings are typed Go-struct fields surfaced as a structured form. (JSONPath remains in the built-in `poll` trigger, which evaluates arbitrary MCP tool output where no per-tool typed schema exists.)

v1 allows one trigger binding per policy. The `trigger:` key remains a single object; multi-trigger generalization (object → list) is deferred until there is a real user requirement.

See **ADR-048** in `ADR_Tracker.md` for full rationale on each of these decisions, and `docs/developer/plugin-system-spec.md` §7 for the complete design.

**Implementation note:** adding a new plugin-sourced trigger event_kind does **not** add a new value to the `TriggerType` enum — `subscribed` covers all plugin-sourced triggers. Steps 1, 6, and 11 of the checklist above are one-time work when `subscribed` itself ships; subsequent plugin event_kinds are additive at the manifest level only and require no changes to the host trigger dispatch path.

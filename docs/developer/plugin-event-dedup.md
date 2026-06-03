# Plugin EmitEvent dedup window — storage design

**Status:** Design pass — must merge before the implementation issue filed off this doc.
**Companion ADRs:** ADR-003 (SQLite/WAL), ADR-013 (ULID IDs).
**Spec sections:** §4.3 (event delivery semantics), §7 (trigger dispatch), §8.1 (`EmitEvent` RPC).

---

## 1. Frame

Spec §4.3 mandates:

> **Event delivery semantics.** `EmitEvent` is at-least-once. Plugins MUST include a stable `event_id` in the event envelope; the host dedupes against a 1-hour rolling window per `(plugin_instance, event_kind, event_id)`. Plugins that cannot synthesize a stable id from the substrate (e.g. a webhook substrate that delivers without a sequence number) should hash the canonical payload as a fallback — duplicate detection outside the stated window is best-effort. Ordering is per-stream (one Trigger `Start` stream preserves order); cross-stream ordering is not guaranteed.

The unsettled sub-question is **where the dedup window lives**: in-memory or SQLite. Settling this unblocks the `event_id` stability guidance issue (#214). Trigger-dispatch routing (#158) is now wired (see below); the remaining work is the dedup store itself, tracked in #215.

The integration point in the current codebase is `EmitEvent` in `internal/plugin/hostsvc/handlers_tier1.go` (the original `handlers.go` was split into `handlers_tier1.go`/`handlers_tier2.go`). `EmitEvent` begins near line 157; `event_id` and `event_kind` non-empty validation are immediately after (around lines 163 and 166); payload marshal begins around line 197. The dedup check inserts after validation and before the `publisher.Publish` / trigger-sink forward. Downstream routing is **already wired**: after publishing `plugin.event_emitted`, the handler forwards the event to the trigger dispatcher via `getTriggerSink()` → `sink.Handle(ctx, evt)` (the old `TODO (#158)` placeholder is gone). The dedup store is still a `Noop` (`internal/plugin/dedup`) until #215 lands the real rolling-window implementation. (Line numbers are approximate — verify against the file before relying on them.)

---

## 2. Volume reality check

Plugin event sources are operator-substrate driven (Slack messages, webhook deliveries, scheduled substrate polls). The spec §4.3 rate limit drops excess events at ingress; RunLauncher concurrency caps apply at egress. This is not high-frequency telemetry. Even a busy Slack integration will produce orders of magnitude fewer events per second than the run-step write rate the DB already handles. DB write cost for dedup is negligible in absolute terms — this is not a performance-sensitive path.

---

## 3. Options

### 3.1 Option A: `sync.Map` + prune goroutine

A `sync.Map` keyed by `(plugin_instance_id, event_kind, event_id)` with a periodic prune goroutine. No DB writes.

**Fatal flaw:** lost on host or plugin restart. A restart within the 1-hour window means the dedup map is empty; events that were already delivered and acted on are treated as novel. §4.3 says the window is 1 hour — it does not say "1 hour unless the host restarts." Violating the at-least-once-with-bounded-redelivery contract is a correctness failure, not a performance one.

### 3.2 Option B: SQLite with `created_at` column

Schema: `plugin_event_dedup(plugin_instance_id, event_kind, event_id, created_at)`. Cleanup via a `DELETE WHERE created_at < now - 1h`. Survives restart.

**Problem:** requires a separate `created_at` column alongside `event_id`. If `event_id` is a ULID (ADR-013 mandates ULID TEXT IDs; `model.NewULID()` is the established helper), the timestamp is already encoded in the first 48 bits. The `created_at` column is redundant duplication that introduces a possibility of skew (clock jitter between creation time and insertion time).

### 3.3 Option C: SQLite, ULID `event_id` as PK component, no `created_at`

Schema uses `(plugin_instance_id, event_kind, event_id)` as the composite primary key. Cleanup is a single lexicographic comparison: `DELETE WHERE event_id < $floor_ulid` where `$floor_ulid` is the ULID encoding of `now - 1h`. The ULID's first 48 bits are the millisecond timestamp; all ULIDs older than the floor sort below it. No secondary index; no `created_at` column.

`github.com/oklog/ulid/v2 v2.1.1` is already present (`go.mod:13`). No new dependency.

---

## 4. Comparison

| | Option A | Option B | Option C |
|---|---|---|---|
| Survives restart | No | Yes | Yes |
| Write cost | 0 (map write) | 1 DB row | 1 DB row |
| Cleanup mechanism | prune goroutine | `DELETE WHERE created_at <` | `DELETE WHERE event_id <` (lexicographic) |
| Extra columns | — | `created_at` | none |
| Complexity | goroutine + map | sweep + created_at | sweep only |
| Correctness vs §4.3 | violates on restart | correct | correct |

---

## 5. Decision: Option C

Option A violates the §4.3 guarantee on host restart and is eliminated.

Between B and C: given ADR-013 mandates ULIDs for all IDs, plugin authors will produce ULID `event_id` values. Encoding the timestamp in the ULID and sweeping lexicographically eliminates the `created_at` column with no loss of correctness. ADR-003 (SQLite/WAL) makes DB writes the established pattern for durable state. The ULID library is already a dep.

**Option C is the decision.**

---

## 6. Schema

```sql
CREATE TABLE plugin_event_dedup (
  plugin_instance_id TEXT NOT NULL
    REFERENCES plugin_instances(id) ON DELETE CASCADE,
  event_kind TEXT NOT NULL,
  event_id   TEXT NOT NULL,
  PRIMARY KEY (plugin_instance_id, event_kind, event_id)
) WITHOUT ROWID;
```

`WITHOUT ROWID` is appropriate here: the row is narrow (three TEXT columns), there are no secondary indexes, and SQLite stores `WITHOUT ROWID` tables as a B-tree keyed directly on the PK — no separate rowid overhead. The implementation PR should run `EXPLAIN QUERY PLAN` on the insert and sweep queries and revisit this choice if the query plan is unexpectedly poor.

**Schema dual-update workflow** (per `docs/developer/database-workflow.md`): `schemas/sql_schemas.sql` is a documentation/reference file — it is not executed by the migration runner. The runtime schema is built by Go migrations in `internal/db/migrations/`. The implementation PR must:

1. Add `internal/db/migrations/0032_add_plugin_event_dedup.go` (modeled on `0031_add_disable_in_app_fallback.go`).
2. Register it in `internal/db/migrations/registry.go`.
3. Mirror the DDL into `schemas/sql_schemas.sql` and `internal/db/migrations/0001_initial.sql` (for fresh installs).
4. Add `sqlc.yaml` schema entry if sqlc needs the new table for type-checking.

The issue body's claim that "schema lives in `schemas/sql_schemas.sql`" is directionally correct but incomplete — the migration Go file is the runtime source of truth.

---

## 7. Cleanup mechanism

### 7.1 Why `internal/timeout.Scanner` is not directly reused

The `timeout.Config` struct (`internal/timeout/scanner.go:32`) is shaped for per-item state-machine transitions: it takes `ListExpired → ExpiredItem → ClaimTimeout → run-state transition` callbacks. Every expired item is claimed individually with a conditional UPDATE to handle concurrent writers racing to claim the same item. That machinery is correct for approval and feedback expiry because each expired item requires a run-state transition and side-effects.

Dedup cleanup is structurally different: it is a single bulk `DELETE WHERE event_id < $floor_ulid` with no per-item state machine. Wrapping a bulk DELETE in the `Config` callback model would require a `ListExpired` that returns every stale row, followed by N individual `ClaimTimeout` calls — which is both wasteful and semantically wrong (there is no "claim" race; rows are just garbage). The *ticker-harness shape* (a `Start(ctx)` method + a `time.NewTicker` loop) is reusable as a pattern, but the `Config` is not.

### 7.2 New package: `internal/plugin/eventdedup`

A small new package with one exported type:

```go
type Sweeper struct {
    db       *db.Store
    interval time.Duration
    metrics  *sweepMetrics
}

func (s *Sweeper) Start(ctx context.Context)
```

`Start` runs a `time.NewTicker(s.interval)` loop. On each tick:

1. Compute `floorULID` = ULID encoding of `now - 1h` with zero entropy.
2. Execute `SweepEventDedup` (`DELETE FROM plugin_event_dedup WHERE event_id < :floor`), collecting `rowsAffected`.
3. Increment `gleipnir_plugin_event_dedup_sweep_deleted_total` by `rowsAffected`.
4. `slog.Info("plugin event dedup sweep", "deleted", rowsAffected)` (only when `rowsAffected > 0` to avoid log noise on quiet installs).

The `RecordEventIfNovel` function wraps the `INSERT OR IGNORE` (`:execrows`). The caller checks `rowsAffected`:

- `1` → novel event, proceed to publish.
- `0` → duplicate, increment `gleipnir_plugin_event_dedup_duplicates_total`, log `slog.Debug`, return `Ok: true` to the plugin without publishing.

### 7.3 Sweep cadence

New environment variable `GLEIPNIR_PLUGIN_DEDUP_SWEEP_INTERVAL`, default `10m`. The sweep deletes rows more than 1 hour old; a 10-minute cadence means the table grows to at most ~6× the per-hour volume before cleanup — negligible for the expected event rates. The implementation PR can adjust the default based on profiling.

---

## 8. Host RPC integration

Insertion point in `internal/plugin/hostsvc/handlers_tier1.go` (`EmitEvent`): after `event_kind` validation, before payload marshal.

Pseudocode:

```go
rows, err := s.eventDedup.RecordEventIfNovel(ctx, inst.ID, req.GetEventKind(), req.GetEventId())
if err != nil {
    return nil, status.Errorf(codes.Internal, "dedup check: %v", err)
}
if rows == 0 {
    // Already seen within the 1-hour window; acknowledge without republishing.
    s.metrics.dedupDuplicates.WithLabelValues(inst.PluginID, inst.ID, req.GetEventKind()).Inc()
    logctx.Logger(ctx).DebugContext(ctx, "plugin event deduplicated",
        "plugin", inst.PluginID, "instance", inst.ID,
        "event_id", req.GetEventId(), "event_kind", req.GetEventKind())
    return &hostv1.EmitEventResponse{Ok: true}, nil
}
// ... existing payload marshal and publish ...
```

The `Sweeper` is constructed in `main.go` alongside the approval and feedback scanners, and its `Start(ctx)` is launched as a goroutine under the root context.

---

## 9. Plugin-author guidance for stable `event_id`

This section coordinates with #214 (SDK-side helper for `event_id` synthesis). The host treats `event_id` as an opaque string; ULID lexicographic ordering is exploited only by the sweep. Plugin authors should follow this priority ladder:

1. **Substrate-native ID wrapped in ULID.** If the substrate provides a sequence number or unique message ID, encode it as a ULID: timestamp from the substrate's authoritative event time, entropy bytes from `sha256(substrate_id)[:10]`. Stable across substrate redelivery of the same message.

2. **Plugin-generated ULID at observation moment.** Acceptable only when the plugin is the sole consumer of the substrate (so no parallel plugin instance can observe the same message and generate a different ULID for it). Timestamp = observation time; entropy = random.

3. **`ulid(now, sha256(canonical_payload)[:10])`.** Fallback for substrates with no usable ID. Deterministic from the payload content; timestamp is the observation time. Collision risk if two distinct events have identical payloads — document this to plugin authors.

The SDK helper (issue #214) should encapsulate options 1 and 3 and make option 2 the easy default for single-consumer plugins.

---

## 10. Observability

Two new counters on `internal/infra/metrics`, following the `gleipnir_plugin_` force-prefix convention from ADR-047:

- `gleipnir_plugin_event_dedup_duplicates_total{plugin, instance, event_kind}` — incremented per deduplicated event.
- `gleipnir_plugin_event_dedup_sweep_deleted_total` — incremented by `rowsAffected` on each sweep tick.

Both are `prometheus.CounterVec` registered in `internal/infra/metrics`. No histograms — the dedup check is a single index lookup; latency tracking adds no value at this event volume.

slog levels: `Debug` per duplicate (high frequency possible under redelivery storms); `Info` per sweep when rows were deleted.

---

## 11. Open questions for the implementation issue

- **Exact sweep cadence:** 10m is the proposal; the impl PR should measure table size under realistic load and adjust.
- **Audit event on duplicate:** lean no — the duplicate metric and debug log give sufficient observability without the DB write overhead of an audit event per duplicate. Reopen if operators report needing audit-trail evidence of dedup activity.
- **`event_id` length cap:** propose 256 bytes. Unlimited lengths would allow degenerate rows. The cap should be enforced in `EmitEvent` validation alongside the non-empty check.
- **`WITHOUT ROWID` revisit:** if `EXPLAIN QUERY PLAN` on the sweep DELETE shows a full-table scan, add a covering index or drop `WITHOUT ROWID`. The plan's expected behavior is a B-tree range scan on the PK prefix.

---

## 12. Test strategy

**`server_test.go`** (where the `TestEmitEvent_*` cases live) — table-driven `EmitEvent` cases:
- First emission of a `(instance, kind, id)` triple → publishes, returns `Ok: true`.
- Second emission of the same triple within 1h → deduplicated, returns `Ok: true`, does not publish.
- Same triple after the sweep has cleared it → treated as novel again.
- Empty `event_id` → `InvalidArgument` (existing coverage, ensure still present).

**`internal/plugin/eventdedup/sweeper_test.go`** — sweeper unit tests:
- Insert rows with ULIDs crafted from `now - 2h` (should be swept) and `now - 30m` (should survive).
- Run `Sweeper.sweep(ctx)` directly (export for testing or use internal package access).
- Assert only the old rows were deleted.

---

## 13. Out of scope

- Cross-instance dedup (§4.3 scopes the window per `plugin_instance`).
- Cross-stream ordering guarantees (§4.3 explicitly disclaims this).
- Persistence beyond 1 hour (the spec window is 1 hour; the sweep enforces it).
- SDK-side `event_id` synthesis helper (tracked in #214).
- Per-event-type trigger routing (#158) — now wired via `getTriggerSink()`/`sink.Handle` in `EmitEvent`; no longer pending.

---

## Appendix A: Implementation follow-up issue body

After this design doc merges, file the implementation issue with the body below. The issue should back-reference #215.

```
Title: Implement plugin EmitEvent dedup (1h rolling window, sqlite + ULID PK)

Implements the design from docs/developer/plugin-event-dedup.md (#215).

## Schema

- Add `internal/db/migrations/0032_add_plugin_event_dedup.go`
  (model on `0031_add_disable_in_app_fallback.go`).
- Register in `internal/db/migrations/registry.go`.
- Mirror DDL into `schemas/sql_schemas.sql` (documentation) and
  `internal/db/migrations/0001_initial.sql` (fresh-install path).
- Add to `sqlc.yaml` schema list.

DDL (from design doc §6):

    CREATE TABLE plugin_event_dedup (
      plugin_instance_id TEXT NOT NULL
        REFERENCES plugin_instances(id) ON DELETE CASCADE,
      event_kind TEXT NOT NULL,
      event_id   TEXT NOT NULL,
      PRIMARY KEY (plugin_instance_id, event_kind, event_id)
    ) WITHOUT ROWID;

Run `EXPLAIN QUERY PLAN` on insert and sweep; revisit `WITHOUT ROWID` if
the query plan is unexpectedly poor.

## sqlc queries

New file `internal/db/queries/plugin_event_dedup.sql`:

- `RecordEventIfNovel :execrows`
  — `INSERT INTO plugin_event_dedup ... ON CONFLICT DO NOTHING`
- `SweepEventDedup :execrows`
  — `DELETE FROM plugin_event_dedup WHERE event_id < :floor`

Pattern: `:execrows` is established by `UpdatePluginPendingRequestStatus`
in `plugin_pending_requests.sql`.

## New package `internal/plugin/eventdedup`

- `Sweeper` struct with `Start(ctx context.Context)` ticker loop.
- Interval from `GLEIPNIR_PLUGIN_DEDUP_SWEEP_INTERVAL` (default 10m).
- `RecordEventIfNovel(ctx, instanceID, eventKind, eventID string) (int64, error)`
  wraps the sqlc call; caller checks rowsAffected.
- NOT built on `internal/timeout.Config` — that struct is per-item state-machine;
  dedup cleanup is a single bulk DELETE (see design doc §7.1).
- Floor ULID uses **zero entropy bytes** so it is the lexicographically smallest
  ULID at `now - 1h` (any non-zero entropy could leave older rows behind in the
  same millisecond): `var id ulid.ULID; _ = id.SetTime(uint64(time.Now().Add(-time.Hour).UnixMilli()))` —
  entropy left at its zero default.

## Integration in `hostsvc/handlers_tier1.go`

Wire `RecordEventIfNovel` between `event_kind` validation (line 405) and
payload marshal (line 407). On `rowsAffected == 0`: increment duplicate
metric, log `slog.Debug`, return `Ok: true` without publishing.

## Metrics

Two new `prometheus.CounterVec` in `internal/infra/metrics`:
- `gleipnir_plugin_event_dedup_duplicates_total{plugin, instance, event_kind}`
- `gleipnir_plugin_event_dedup_sweep_deleted_total`

## `main.go` wiring

Construct `Sweeper` alongside approval/feedback scanners; launch
`sweeper.Start(ctx)` as a goroutine under the root context.

## Tests

- `internal/plugin/hostsvc/server_test.go` (alongside the existing
  `TestEmitEvent_*` cases): table-driven `EmitEvent` cases covering
  novel, duplicate, and post-sweep-novel paths.
- `internal/plugin/eventdedup/sweeper_test.go`: insert rows with crafted
  past/present ULIDs; assert only rows > 1h old are swept.

## Out of scope

- SDK-side `event_id` synthesis helper (#214).
- Per-event-type trigger routing (#158).
- Cross-instance or cross-stream dedup.
- `event_id` length cap (propose 256 bytes; validate in EmitEvent alongside
  the existing non-empty check — can land in this PR or a follow-up).
```

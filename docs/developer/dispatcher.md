# Scheduler Dispatcher — Design

This document describes the centralized scheduler dispatcher introduced by [ADR-036](ADR_Tracker.md#adr-036-centralized-scheduler-dispatcher). It is the design reference for anyone adding a new timed primitive, modifying the scheduled or poll triggers, or migrating to a multi-node HA implementation.

## Purpose

Consolidate "do work at time T" across Gleipnir's trigger subsystems into one package. Before this design, `Scheduler` and `Poller` each owned an independent lifecycle, reconcile plumbing, and `PolicyNotifier` implementation. Every new timed primitive paid the same ~100-line tax. The dispatcher collapses that tax into a single leaf package with a narrow interface.

## What the dispatcher is — and is not

The dispatcher is:

- A **single in-memory index** of pending scheduled work, keyed on fire time.
- A **single goroutine** that wakes at the top of the index and dispatches work to registered handlers.
- A **leaf package** (`internal/dispatcher/`) with zero imports from other `internal/*` packages. Handlers are registered from `main.go` and close over `RunLauncher`, `Store`, and so on.

The dispatcher is not:

- A durable job queue. The heap is in-memory; it is rebuilt on startup by scanning existing tables (`policies`, policy YAML). A crash between fire and handler completion loses that fire, the same property as the pre-dispatcher code.
- An event bus. It does not carry arbitrary domain events; it only manages scheduled work.
- A worker pool. There is one goroutine managing the heap. Handler invocations spawn their own child goroutines so that a slow handler cannot delay the next dispatch.
- Coupled to any specific domain concept. It knows `kind` strings and opaque `payload any` values. It does not know what a run, a policy, or an approval is.

## Architectural diagrams

### Before — two subsystems, duplicated plumbing

```
┌──────────────────────────────┐   ┌──────────────────────────────┐
│         Scheduler            │   │            Poller            │
│      (scheduled.go)          │   │          (poll.go)           │
│                              │   │                              │
│  N goroutines, one per       │   │  1 goroutine per active      │
│  fire_at timestamp           │   │  poll policy                 │
│                              │   │                              │
│  + reconcile from startup    │   │  + 1 reconcile goroutine     │
│  + PolicyNotifier.Notify     │   │    (60s interval)            │
│  + rootCtx handoff           │   │  + PolicyNotifier.Notify     │
│  + mu, timers map            │   │  + rootCtx handoff           │
│                              │   │  + mu, loops map, wg         │
└──────────────┬───────────────┘   └──────────────┬───────────────┘
               │                                  │
               ▼                                  ▼
         RunLauncher.Launch                 RunLauncher.Launch
         (+ CheckConcurrency,               (+ MCP tool calls,
           Enqueue, pauseIfExhausted)         check evaluation,
                                              CheckConcurrency,
                                              Enqueue)

Every new timed primitive would add a third box with the same plumbing.
```

### After — one dispatcher, handlers registered by kind

```
                 ┌────────────────────────────────────────┐
                 │        internal/dispatcher             │
                 │                                        │
                 │  ┌──────────────────────────────────┐  │
                 │  │   Dispatcher (interface)         │  │
                 │  │                                  │  │
                 │  │   Schedule(fireAt, kind,         │  │
                 │  │            payload) → jobID      │  │
                 │  │   Cancel(jobID)                  │  │
                 │  │   RegisterHandler(kind, fn)      │  │
                 │  └──────────────────────────────────┘  │
                 │                 │                      │
                 │                 ▼                      │
                 │  ┌──────────────────────────────────┐  │
                 │  │       memoryDispatcher           │  │
                 │  │                                  │  │
                 │  │   min-heap keyed on fireAt       │  │
                 │  │   1 goroutine: sleep → fire      │  │
                 │  │   handlers map[kind]HandlerFn    │  │
                 │  │   wake chan struct{}             │  │
                 │  │   jobs map[id]*job               │  │
                 │  └──────────────────────────────────┘  │
                 └──────────────▲──────────────┬──────────┘
                                │              │
                   Schedule(…)  │              │  fire(ctx, payload)
                                │              ▼
       ┌────────────────────────┼──────────────┬─────────────────────┐
       │                        │              │                     │
  policy save             approval            scheduled_fire       poll_tick
  / update path           creation            handler              handler
  (scheduled              (follow-up          → RunLauncher        → MCP checks
   policies:              migration,          .Launch()            → RunLauncher
   1 Schedule per         out of scope                             .Launch on match
   fire_at)               for initial         after fire: nothing  → Schedule next
  (poll policies:         dispatcher           (fire_at is a          tick (self-
   1 Schedule for          work)               one-shot)              rescheduling)
   first tick)
```

### What stays the same

```
┌───────────────────────────────────────────────────────────────┐
│        Agent run execution (UNCHANGED by this design)         │
│                                                               │
│  RunManager ─┬─ map[runID]*trackedRun                         │
│              └─ 1 goroutine per active run                    │
│                 └── LLM loop: prompt → tool → audit → repeat  │
│                                                               │
│  AuditWriter queue         SSE Broadcaster                    │
│  Approval channel          Feedback channel                   │
│  Timeout scanners (internal/timeout/, approval/, feedback/)   │
└───────────────────────────────────────────────────────────────┘
```

## Interface

```go
package dispatcher

import (
    "context"
    "time"
)

// Dispatcher schedules work to run at future times and invokes
// registered handlers when that time arrives. Implementations are
// concurrency-safe.
type Dispatcher interface {
    // Schedule pushes a job onto the queue. The handler registered
    // for kind will be invoked at or shortly after fireAt, in a
    // child goroutine, with the supplied payload. Returns a job ID
    // usable with Cancel.
    Schedule(fireAt time.Time, kind string, payload any) int64

    // Cancel marks the job as canceled. If the job has not yet
    // fired, its handler will not be invoked. Canceling a job that
    // has already fired or been canceled is a no-op.
    Cancel(jobID int64)

    // RegisterHandler associates a handler with a kind. Must be
    // called before Schedule is used for that kind. Typically
    // invoked from main.go during startup; handlers are not expected
    // to change at runtime.
    RegisterHandler(kind string, fn HandlerFn)
}

// HandlerFn is the function invoked when a scheduled job fires. It
// runs in its own goroutine so that a slow handler does not block
// further dispatch. The context is the dispatcher's root context;
// handlers should respect cancellation for graceful shutdown.
type HandlerFn func(ctx context.Context, payload any)
```

### Handler contract

- **Idempotency at fire time.** A handler may be invoked after the underlying state (policy, approval, etc.) has changed. Handlers re-check status before acting and drop the fire cleanly if the work is no longer applicable.
- **Own goroutine.** Each invocation runs in a new goroutine. Handlers do not need to return quickly; the dispatcher is already onto the next fire.
- **Panic safety.** The dispatcher wraps each invocation in a `recover()`. Handler panics log an error and are swallowed. They do not crash the dispatcher.
- **No assumed ordering across kinds.** Two jobs with the same `fireAt` but different `kind` may dispatch in either order.
- **Self-rescheduling is the pattern for recurring work.** A `poll_tick` handler schedules its next tick at the end of its run. No "reconcile every N seconds" loop is needed.

### Payload discipline

The `payload any` is opaque to the dispatcher. Handlers cast to a known struct:

```go
type scheduledFirePayload struct {
    PolicyID string
    FireAt   time.Time
}

type pollTickPayload struct {
    PolicyID string
}
```

Payloads should be small, self-contained value types. Do not put database handles, MCP clients, or other live resources in a payload — those are captured by the handler closure at registration time.

## `memoryDispatcher` internals

### State

```go
type memoryDispatcher struct {
    mu       sync.Mutex
    heap     jobHeap              // container/heap min-heap on fireAt
    handlers map[string]HandlerFn // registered at startup, read-only after
    wake     chan struct{}        // buffer 1: "heap top may have changed"
    nextID   atomic.Int64
    jobs     map[int64]*job       // id → job, for O(1) cancel
    clock    Clock                // time.Now abstraction for fake-clock tests
}

type job struct {
    id       int64
    fireAt   time.Time
    kind     string
    payload  any
    canceled atomic.Bool
}
```

### Main loop

```
loop:
    wait = time.Until(heap.top.fireAt)   // or longSleep if heap is empty
    select:
        case <-ctx.Done:
            return
        case <-wake:
            // re-plan: a new earlier job was scheduled, or top was canceled
            continue
        case <-timer.C:
            fireReady(now)
            continue

fireReady(now):
    under mu:
        pop all jobs j where j.fireAt <= now
        remove them from jobs map
        collect non-canceled into ready[]
    for each j in ready:
        go runHandler(j.kind, j.payload)  // child goroutine, panic-recovered
```

### Why each mechanism exists

- **`wake` channel (buffered 1, non-blocking send).** Without it, `Schedule` of a job earlier than the current heap top would not interrupt the goroutine's sleep. The channel is a "signal, not a queue" — multiple concurrent Schedules collapse into one wake.
- **`canceled` atomic flag, not heap removal.** Removing from a heap is O(n). Flipping a flag and skipping at fire time is O(1). Canceled jobs fall out naturally when their `fireAt` is reached.
- **`jobs` map.** Provides O(1) lookup for `Cancel(jobID)`. Without it, cancel would scan the heap.
- **Child goroutine per handler invocation.** A handler may do real work: launch a run, call MCP tools, write to the DB, block on an LLM call. Running it inline would delay the next scheduled fire. The child goroutine is the only place new goroutines are created, and they are created at the moment work is actually needed — not N of them sitting idle waiting for tickers.
- **`Clock` interface.** Tests construct a fake clock and drive time deterministically. The production implementation is a trivial wrapper around `time.Now` and `time.NewTimer`.

## Startup and shutdown

### Startup sequence

1. `main.go` constructs `memoryDispatcher` and calls `dispatcher.Start(ctx)`.
2. Handlers are registered for each `kind` the process understands:
   ```go
   disp.RegisterHandler("scheduled_fire", scheduledFireHandler)
   disp.RegisterHandler("poll_tick",      pollTickHandler)
   ```
3. The dispatcher goroutine launches and blocks on an empty heap.
4. Seed logic reads existing tables and populates the heap:
   ```
   for each active scheduled policy:
       for each future fire_at:
           disp.Schedule(fireAt, "scheduled_fire", {policyID, fireAt})

   for each active poll policy:
       disp.Schedule(now + interval, "poll_tick", {policyID})
   ```
5. HTTP server starts. Policy save paths call `Schedule()` synchronously on mutation.

### Shutdown sequence

1. Main context is canceled.
2. Dispatcher goroutine observes `<-ctx.Done`, exits its main loop.
3. Child handler goroutines are given the same canceled context. They either complete quickly or are abandoned along with their parent process.

The dispatcher does not attempt to drain in-flight handlers. Handlers that need clean shutdown honor `ctx.Done()` themselves, consistent with the rest of the codebase.

## Example flow: scheduled policy fires

```
 t0              t0+Δ             t1 = fire_at
  │               │                  │
  │               │                  │
  ▼               │                  │
operator POST     │                  │
/api/v1/policies  │                  │
  │               │                  │
  ├─► policy_handler.Create          │
  │     │                            │
  │     ├─► store.InsertPolicy       │
  │     │                            │
  │     └─► dispatcher.Schedule(     │
  │           fire_at=t1,            │
  │           kind="scheduled_fire", │
  │           payload={policyID,     │
  │                     fireAt=t1})  │
  │           │                      │
  │           ├─► heap.Push          │
  │           └─► wake <- {} (if     │
  │                 new top)         │
  │                                  │
  ▼                                  ▼
  201 Created               dispatcher goroutine
                            (was sleeping):
                              <-timer.C at t1
                              pop job from heap
                              go scheduledFireHandler(
                                  ctx, payload)
                                       │
                                       ├─► re-check policy status
                                       │   (paused? deleted? → drop)
                                       │
                                       ├─► RunLauncher.CheckConcurrency
                                       │
                                       ├─► RunLauncher.Launch or Enqueue
                                       │
                                       └─► pauseIfExhausted
                                           (if no more future fire_at)
```

Compare to the pre-dispatcher code: policy creation had to call `Scheduler.Notify()`, which in turn re-loaded the policy from DB, parsed it, and armed per-`fire_at` goroutines. The reconcile goroutine served as a safety net. In the new model, there is no reconcile, no notify, no safety net — the `Schedule` call is the event.

## How to add a new timed primitive

Most of what previously required a new package, a new goroutine lifecycle, and a new reconcile loop now fits into three changes:

1. **Register a handler** in `main.go`:
   ```go
   disp.RegisterHandler("my_new_kind", myHandler)
   ```
2. **Seed from DB** during startup if pending instances may already exist:
   ```go
   for _, row := range store.GetPendingMyThings(ctx) {
       disp.Schedule(row.FireAt, "my_new_kind", myPayload{ID: row.ID})
   }
   ```
3. **Schedule from the create path** for each new instance:
   ```go
   disp.Schedule(fireAt, "my_new_kind", myPayload{ID: newID})
   ```

The handler itself handles re-checking status at fire time and (if recurring) self-rescheduling. No new package, no new goroutine lifecycle.

## Multi-node HA path

The `Dispatcher` interface is the seam. Future implementations behind the same interface:

- **`leaderOnlyDispatcher`** — a lightweight leader election (e.g. via a database lock or an external lease) elects one node to run the dispatcher goroutine. Follower nodes accept `Schedule` calls and forward them to the leader. Simplest HA design; no coordination on dispatch itself.
- **`raftDispatcher`** — the job set is replicated via Raft. Any node can dispatch; the Raft log ensures single-fire semantics under failure. Highest complexity.
- **External primitive** — delegate to NATS JetStream, Temporal, or similar. The dispatcher becomes a thin adapter.

All options reuse the existing callers and handlers verbatim. The in-memory choice for single-node Gleipnir does not accrue migration debt against any of these options, because the source of truth already lives in the existing domain tables.

## References

- [ADR-036 — Centralized scheduler dispatcher](ADR_Tracker.md#adr-036-centralized-scheduler-dispatcher) — the decision record
- [ADR-014 — Poll trigger MCP client architecture](ADR_Tracker.md) — the poll trigger's original design
- [ADR-016 — SSE over WebSockets](ADR_Tracker.md#adr-016-real-time-ui-transport-sse-over-websockets) — shares the "interface-based, swappable for HA" design principle
- [`docs/developer/adding-a-trigger-type.md`](adding-a-trigger-type.md) — will be updated alongside Phase 2 of the migration to reflect the dispatcher-based handler pattern

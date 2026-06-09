# Plugin generation lifecycle, drain, and shutdown

**Status:** Design pass — must merge before #187 (fsnotify watcher), #189 (material-change detection), and the implementation issues filed off this doc.
**Companion ADRs:** ADR-038 (atomic run-state transitions), ADR-041 (plugin system umbrella), ADR-044 (channel routing — `request_id` is instance-scoped, not generation-scoped), ADR-045 (signing & TOFU trust).
**Spec sections:** §3.2 (plugin vs instance vs generation), §4.2 (Channel `Request` lifecycle across generations), §5.1 (hot-reload), §13.3 (generation stacking), §13.4 (shutdown).

This document is the design pass for issues #190 (generation reference counting + drain semantics) and #193 (wiring plugin subprocess shutdown into ADR-038 graceful-shutdown). They land together because hot-reload drain and host shutdown share the same primitives — at shutdown time, *every* live generation enters the same drain path that hot-reload uses for one.

The frame for this whole doc is **delta from `hashicorp/go-plugin`**. Every section starts by enumerating what go-plugin already gives us, then describes only what the plugin work adds on top. Re-implementation is rejected by default.

---

## 1. What `hashicorp/go-plugin` already provides

This is the load-bearing question for both issues. Anything in this list is not in scope for the plugin work — we use the library API directly.

**Subprocess lifecycle.** `plugin.Client` spawns the subprocess, manages its stdio, captures stderr line-by-line, and tears it down on `Client.Kill()`. The library handles the cookie-based handshake (so a stranger process can't impersonate a plugin) and the version-negotiation handshake (which we *will* layer our own per-service version pins on top of, but the underlying handshake transport is theirs).

**gRPC client lifetime.** `Client.Client()` returns a connected gRPC client. The library owns the connection: opens it after handshake, closes it on `Kill()`, and surfaces connection-loss as a typed error to in-flight RPCs. We do not write our own gRPC dialer.

**Standard health probing.** `Client.Ping()` issues a `grpc.health.v1.Health.Check`. Spec §13.5 calls for 30s ping, 3 missed → restart; that's a host-side loop calling `Ping()` and counting failures, *not* a re-implementation of health.

**Graceful shutdown — SIGTERM with grace, then SIGKILL.** `Client.Kill()` sends SIGTERM, waits up to a configurable grace, and SIGKILLs stragglers. The grace defaults match what spec §13.4 wants (10s) for the per-process tail; we use the library's value rather than hand-rolling signal sequencing.

**Stdio capture & log routing.** Plugin stdout/stderr are captured per-line and routed to a host-side logger. Spec §12.1 wants `plugin=<name> instance=<instance>` line prefixes; that's a logger middleware decision, not a transport concern.

**What go-plugin does *not* provide** (and therefore *is* in scope for this design):
1. Per-call reference counting that survives the gRPC client being replaced. The library's gRPC client is bound to one subprocess; on hot-reload we get a *different* `*plugin.Client`, and tracking which in-flight runs still hold the old one is on us.
2. Channel `Request` callback handoff between generations. The `request_id` is instance-scoped (ADR-044 §4) and must remain valid as a different subprocess takes over. go-plugin has no concept of "instance" — it knows about clients.
3. The single-old-generation cap (spec §13.3). go-plugin lets you start a new client without telling the old one anything.
4. Run-state transitions (`interrupted` per ADR-011 / ADR-038). go-plugin doesn't know what a Gleipnir run is.
5. The 60s force-kill grace for a draining old generation. The library's `Kill()` grace is for a single shutdown; we want a *separate* timer that fires once a generation has been *replaced* and refuses to drain.

Everything below is the on-top layer for those five gaps.

---

## 2. Generation lifecycle (the on-top piece)

### 2.1 Three generation states

Each `*plugin.Client` instance the host holds is wrapped in a `Generation` value with a state machine of three states:

```
   active  ──[hot-reload]──►  draining  ──[refcount=0 OR 60s]──►  killed
     ▲                                                              │
     └─────────────────────────[next active gen takes over]─────────┘
```

- **`active`** — the canonical generation for an instance. New tool calls and channel operations route here. There is exactly one active generation per instance at any moment (or zero, in error states).
- **`draining`** — replaced by a newer generation. No new calls dispatch to it. In-flight calls finish; once the refcount hits zero (or the 60s timer fires), the generation transitions to `killed`. At most one draining generation per instance (spec §13.3).
- **`killed`** — `plugin.Client.Kill()` has been called. The subprocess is dead or about to be (SIGTERM grace then SIGKILL via go-plugin). Any pending callbacks for this generation now flow through the late-callback path (ADR-046).

The state lives in memory only; it is not persisted in the `plugin_instances` row. The DB-backed `health_state` enum (ADR-045 §7) is admin-facing and concerns the *instance* (e.g. `pending_key_approval`), not the *generation*.

### 2.2 Reference counting unit: per-RPC, with a `call_id`

The reference counted unit is **one outbound RPC** to the plugin (one `ToolService.Call` invocation, one `ChannelService.Notify`, one `ChannelService.Request` until it pre-acks). Each carries the `call_id` introduced in spec §8.5 and consumed by §13.8 cancellation.

Mechanics:

1. The host wraps every dispatch in `gen.Acquire()` → returns a `release func()`. `Acquire()` increments an `atomic.Int64` on the generation; `release()` decrements. `release()` is `defer`-ed at the dispatch call site.
2. When the host hot-reloads, it transitions the active gen to `draining`. New `Acquire()` calls on the draining gen are *rejected* (the dispatcher checks state before acquiring). Already-acquired calls hold their reference until they return.
3. When the refcount of a draining gen hits zero, the generation drains *immediately* — no need to wait for the 60s timer. The timer is only the upper bound.

We pick per-RPC over per-call_id because the call_id is itself per-RPC for ToolService and Notify. For Channel `Request` (which is async via callback), the reference is held only until pre-ack, *not* until the human responds — see §2.4 below for why.

A `sync.WaitGroup` is the obvious primitive, but `atomic.Int64` + a `chan struct{}` "drained" signal is cleaner here because we want to *check* the count without blocking (the dispatcher needs to fail fast on a draining generation; `WaitGroup.Wait` blocks).

### 2.3 The 60-second force-kill grace timer

Started **the moment a generation transitions to `draining`** (i.e. the moment hot-reload registers the replacement). At expiry the host calls `plugin.Client.Kill()` on the draining generation regardless of refcount. Any RPCs still holding references will see the gRPC connection drop and surface a `tool_error` step (per spec §13.7 — same shape as MCP failure).

The 60s figure comes from spec §13.3. We do not re-derive it. It is *not* the same number as ADR-038's 10s graceful-shutdown timeout — the drain timer is per-replaced-generation; the 10s is per-host-shutdown. Both can be in flight simultaneously: see §3.

The timer is a single `time.AfterFunc` per draining generation. There is no need for a polling loop.

### 2.4 Channel `Request` callback handoff

This is the weirdest case. ADR-044 §4 nails it down: `request_id` is instance-scoped, not generation-scoped. The host stores enough state in `feedback_requests` (per the existing schema) and a host-side `request_id → instance_id` map to resolve a callback regardless of which generation is alive when it arrives.

What this design adds on top:

1. **Refcount drops at pre-ack, not at human response.** When generation N issues a `Request` to a plugin and the plugin pre-acks within 5s, `release()` is called. The reference is gone *even though the run is still in `waiting_for_feedback`*. This is intentional: a draining generation must be allowed to drain even if it has issued requests that haven't been answered. Otherwise a human leaving for the weekend pins the old generation alive for the entire feedback timeout window.

2. **Late callback dispatch is generation-agnostic.** When a `WriteAuditStep(request_id, feedback_response)` arrives, the host looks up the `request_id` against the per-instance map, *not* against any specific generation. If the active generation's identity doesn't match the connection that issued the call (which is the normal case after a hot-reload), the host services the callback against the active generation's gRPC connection — not the dead one. The plugin author's responsibility is to make `WriteAuditStep` callable from any generation that holds a fresh substrate handle (Slack token, etc.); ADR-044 §4 already requires this.

3. **Force-kill at 60s does NOT eagerly fail outstanding `Request`s.** ADR-044 §4 is explicit: runs in `waiting_for_feedback` are not eagerly failed. The existing `internal/timeout/scanner.go` machinery owns expiry. If the human eventually replies and the substrate is recoverable on the new generation, the late callback resolves cleanly. If not, the timeout scanner fires and the run takes the timeout outcome.

4. **The host emits a `generation_drain_with_open_requests` audit event** when a draining generation force-kills with non-zero pending `Request` records. Severity `warning`. This is not a failure — it's operator-visible context for "a hot-reload happened while N humans had open feedback requests; here's how many got recovered vs. timed out." The event lands in `plugin_audit_events` per ADR-046.

### 2.5 What blocks a second hot-reload while one is draining

Spec §13.3: at most one draining old generation per plugin. A second hot-reload while the previous old generation is still draining queues the new generation start until the old drains or hits the 60s force-kill grace.

Mechanics: a per-instance `sync.Mutex` held by the hot-reload coordinator. The fsnotify dispatcher (#187) calls into a `pluginManager.HotReload(instanceID, newPath)` method that grabs the mutex, checks for a draining generation, and either:
- Waits on the draining generation's `drained` channel (closed when refcount hits zero or the 60s timer fires).
- Logs a `hot_reload_queued` info-severity event and *continues to wait* — does not return error. Operator-visible via `plugin_audit_events`.

We don't drop the second hot-reload (operator pasted a new tarball, they expect it to land). We don't error out either — the queue is short, bounded by the 60s grace.

If a *third* hot-reload arrives while the second is queued, we collapse: the third replaces the second's intent (the queued path becomes the third's path; the queued semaphore counts at most 1). The intermediate version is skipped silently — operators dropping multiple builds in rapid succession get the latest one, which is what they want.

### 2.6 Interaction with `max_concurrent_calls` semaphore

`max_concurrent_calls` is per-instance (Phase 4 work, spec §13.2). It is **not** per-generation. The semaphore lives on `pluginInstance`, not on `generation`, and is acquired *before* a generation reference is taken. Sequence:

```
dispatcher
  ↓
  acquire instance.semaphore   ← Phase 4 throttle
  ↓
  pick active generation
  ↓
  gen.Acquire()                ← refcount on the chosen generation
  ↓
  RPC
  ↓
  defer release() & semaphore.Release()
```

This means a hot-reload doesn't change the throttle — operators sized the cap based on what the *substrate* (Slack, downstream HTTP) can absorb, not based on which subprocess generation is alive. Phase 4's queueing-with-deadline behavior under throttle pressure is unchanged.

---

## 3. Shutdown wiring (#193 — delta from existing graceful shutdown)

### 3.1 What main.go's shutdown already does

From `main.go:323-360`, the existing sequence on SIGTERM/SIGINT:

1. Cancel the root context. Scheduler, poller, cron, background timers stop.
2. `runManager.CancelAll()` — every in-flight agent run is told to stop. Run contexts derive from `context.Background()` (intentionally), so the root cancel does *not* cancel them; CancelAll explicitly does.
3. `select { runsDrained / DrainTimeout }` — wait for poll loops, cron, and run goroutines to exit, with a hard cap at `cfg.DrainTimeout`.
4. `srv.Shutdown(shutdownCtx)` — HTTP server graceful close (5s).
5. `httpWG.Wait()` — listener goroutine joined.

ADR-011 / ADR-038 already handle the run-side: any run still in `running`, `waiting_for_approval`, or `waiting_for_feedback` at shutdown is left in those states; the next startup scan transitions them to `interrupted`.

### 3.2 The delta the plugin work needs to add

Just one new step, between (3) and (4):

**3a. Drain plugin generations.** For every plugin instance, transition active generations to `draining`, then `pluginManager.WaitDrained(shutdownCtx)` blocks until refcounts hit zero or the host-shutdown grace fires. Same drain machinery as §2 — shutdown is just "every generation hot-reloads to nothing."

The grace is **the existing `cfg.DrainTimeout`**, not a new timer. ADR-038's 10s figure is the right shape for a single coordinated shutdown; a draining generation at host-shutdown does *not* get its own additional 60s on top — that would push total shutdown past most container orchestrators' kill windows. Concretely: the 60s per-generation timer from §2.3 is irrelevant during shutdown because shutdown's own timeout is shorter and dominates.

**3b. SIGTERM plugin subprocesses via `plugin.Client.Kill()`.** Done after the drain wait completes (or its timeout fires). go-plugin handles the SIGTERM-grace-then-SIGKILL; we just call `Kill()` per generation. Order does not matter across instances.

**3c. Skip step (3a) if `GLEIPNIR_PLUGINS_ENABLED=false`.** No-op. The flag is checked at startup; the plugin manager doesn't exist when disabled, so its shutdown method is a nil-receiver no-op or a guarded call site.

### 3.3 Order of operations (consolidated)

The full shutdown sequence after this design:

```
SIGTERM/SIGINT
  ↓
1. cancel root context           (existing — stops scheduler, poller, cron)
  ↓
2. runManager.CancelAll()        (existing — propagates to in-flight runs)
  ↓
3. wait runs+poll+cron drained   (existing — DrainTimeout-bounded)
  ↓
3a. plugin generations → draining (NEW)
  ↓
3b. wait plugin refcounts → 0    (NEW — same DrainTimeout shares budget; no separate clock)
  ↓
3c. plugin.Client.Kill() each    (NEW — go-plugin handles SIGTERM/SIGKILL)
  ↓
4. srv.Shutdown(5s)              (existing — HTTP close)
  ↓
5. httpWG.Wait()                 (existing — listener join)
```

Step 3 must come before 3a/3b so runs have a chance to release plugin references cleanly. CancelAll cancels each run's context; the run's deferred `release()` calls fire as the goroutines unwind. By the time we transition generations to `draining`, the refcount should already be tumbling.

Trigger streams (`TriggerService.Start`) are long-lived gRPC streams. Cancellation propagates through go-plugin's connection close at step 3c. There is no separate "close trigger streams" step — closing the gRPC connection takes the stream with it.

### 3.4 Run-state outcome at shutdown

Unchanged from ADR-011 / ADR-038:

- A run in `running` whose plugin tool call was outstanding when shutdown fired sees its context cancelled (step 2). The run goroutine returns; status is left in `running`; startup scan transitions to `interrupted`.
- A run in `waiting_for_feedback` is *not* cancelled by step 2 (the run goroutine isn't running — it's parked). At startup it's marked `interrupted` per the existing ADR-011 scan.
- The `feedback_response_late` event from ADR-044 fires if a substrate later delivers a callback for a run that's now `interrupted` — no special-casing needed; the late-callback path already handles this.

No new run-state. No new step type. The existing `interrupted` machinery covers plugin-bearing runs identically.

### 3.5 What we explicitly do NOT add

- **A separate "drain trigger streams" step.** Subsumed by gRPC connection close.
- **A separate "fail outstanding Channel Requests at shutdown" step.** They survive into the next startup as open `feedback_requests` rows, in `interrupted` runs; the timeout scanner expires them on schedule.
- **A new run state for "interrupted because a plugin generation was force-killed."** ADR-011's `interrupted` already means "the host went away mid-run"; the cause (in-process work, MCP call, or plugin call) is irrelevant to the operator surface. The cause shows up in the `plugin_audit_events` audit trail, not in the run's status.
- **Any retry on shutdown.** Best-effort drain, then kill. Operators relaunch the host and the startup-scan transitions handle resumption per existing semantics.

---

## 4. Implementation issues filed off this design

The acceptance criteria of #190 and #193 say "after design: file follow-up implementation issue(s) and link them here." Implementation lands as:

- **#190-impl** — `internal/plugin/manager` core: `Generation`, refcount primitive, drain machinery, hot-reload coordinator, per-instance mutex. No fsnotify yet; tests use direct `manager.HotReload(...)` calls.
- **#193-impl** — `main.go` shutdown wiring (steps 3a/3b/3c). Lands after #190-impl since it depends on the manager API.
- (Existing #187) fsnotify watcher consumes `manager.HotReload` once the manager is in tree.
- (Existing #189) material-change detection is the gate before `manager.HotReload` is even called.

These can be filed once this design lands — they are not blocked by reviewer pre-approval of every interface name.

---

## 5. Open questions deferred to v2

- **Per-policy concurrency caps interacting with plugin generations.** Today `max_concurrent_calls` is per-instance. Per-policy caps would multiply the bookkeeping; out of scope.
- **Recovering a Channel `Request` substrate connection across host restarts.** ADR-044 §4 punts on this; a `RecoverChannelRequests` RPC is deferred. Until then, host restart = open requests time out via the scanner.
- **Live migration of an instance's substrate state across plugin updates.** Not in v1. Hot-reload of a plugin that needs substrate continuity (e.g. a long-running connection pool) is the plugin author's problem.
- **Distinct backoff for cgroup-style hard limits (spec §13.1).** Hard cgroup-per-subprocess is v2; the soft `GOMEMLIMIT` advisory + RSS sampling is the v1 ceiling.

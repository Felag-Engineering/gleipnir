# Gleipnir — ADR Tracker

Running index of all Architecture Decision Records. Promote items from the Roadmap here when decided. Link to the full ADR entry in the README when written.

---

## Status Key

- 🟢 **Decided** — resolved, implemented or ready to implement
- 🟡 **In progress** — being actively discussed
- 🔴 **Unresolved** — needs a decision before building the affected component
- ⬜ **Deferred** — deliberately not v1

---

## ADR Index

| #       | Title                                              | Status        | Phase  | Affects                                              |
|---------|----------------------------------------------------|---------------|--------|------------------------------------------------------|
| ADR-001 | Hard capability enforcement at runtime             | 🟢 Decided    | v0.1   | MCP registry, policy engine, agent runtime           |
| ADR-002 | Policy-as-YAML stored in DB                        | 🟢 Decided    | v0.1   | Policy engine, UI editor                             |
| ADR-003 | SQLite for storage                                 | 🟢 Decided    | v0.1   | Storage layer, audit queue                           |
| ADR-004 | MCP-native, HTTP transport first                   | 🟢 Decided    | v0.1   | MCP client, tool registry                            |
| ADR-005 | Go + chi + sqlc backend                            | 🟢 Decided    | v0.1   | Backend architecture                                 |
| ADR-006 | React frontend, embedded in Go binary              | 🟢 Decided    | v0.1   | Frontend, go:embed, Docker Compose                   |
| ADR-007 | BoundAgent: sensor / actuator / feedback roles     | 🟢 Decided    | v0.1   | Policy schema, runtime, UI                           |
| ADR-008 | Two approval modes (agent-initiated + policy-gated)| 🟢 Decided    | v0.2   | Approval interceptor, feedback channel               |
| ADR-009 | Feedback channel: policy-first, system fallback    | 🟢 Decided    | v0.2   | Policy schema, notification system                   |
| ADR-010 | Project name: Gleipnir                             | 🟢 Decided    | —      | —                                                    |
| ADR-011 | v1 approval path (UI vs Slack callbacks)           | 🟡 In Progress | v0.2   | Approval UX, inbound network model                   |
| ADR-012 | Run persistence and recovery behavior              | 🟢 Decided    | v0.1   | Run executor, storage layer, startup sequence        |
| ADR-013 | System prompt default template                     | 🟢 Decided    | v0.1   | Agent runtime, policy schema, UI prompt editor       |
| ADR-014 | Poll trigger MCP client architecture               | 🟢 Decided    | v0.3   | Trigger engine, MCP client, package structure        |
| ADR-015 | Policy concurrency model                           | 🟢 Decided    | v1.0   | Trigger engine, run executor, policy schema          |
| ADR-016 | Real-time UI transport: SSE over WebSockets        | 🟢 Decided    | v0.1   | Frontend, Go API, HA scaling path                    |
| ADR-017 | Policy-level parameter scoping for MCP tools       | 🟢 Decided    | v0.1   | Policy schema, MCP client, agent runtime, audit log  |
| ADR-018 | Capability snapshot as first run step              | 🟢 Decided    | v0.1   | Run steps schema, agent runtime, reasoning timeline  |
| ADR-019 | Dual-mode policy editor (form + YAML)             | 🟢 Decided    | v0.1   | Frontend, policy YAML schema                         |
| ADR-020 | Policy folders for UI grouping                    | 🟢 Decided    | v0.1   | Policy YAML schema, frontend dashboard               |
| ADR-021 | MCP discovery diffs                               | 🟢 Decided    | v0.1   | MCP discovery endpoint, frontend                     |
| ADR-022 | Transport-level fake for Anthropic API in tests   | ⬜ Deferred   | v0.1   | agent package, integration tests                     |
| ADR-023 | Per-policy model selection                         | 🟢 Decided    | v0.1   | Policy schema, agent runtime, capability snapshot    |
| ADR-024 | Webhook HMAC-SHA256 signature verification         | 🟢 Decided    | v0.1   | Webhook handler, policy schema, trigger package      |
| ADR-026 | Model-agnostic design (multi-provider) — revised   | 🟢 Decided    | v1.0   | LLM client interface, agent runtime, policy schema   |
| ADR-028 | Tool risk classification model                     | 🟢 Decided    | v1.0   | Policy schema, runtime approval interceptor          |
| ADR-029 | Approval state machine (v1.0 minimal)              | 🟢 Decided    | v1.0   | BoundAgent runtime, approval handler, SSE, UI        |
| ADR-030 | UI abstracts over tool transport — Tools page is protocol-agnostic | 🟢 Decided | v0.1 | Frontend nav, routes, MCPPage UI text          |
| ADR-031 | Native feedback as a Gleipnir runtime primitive | 🟢 Decided | v1.0 | Agent runtime, policy schema, notify package, UI |
| ADR-032 | Admin-managed OpenAI-compatible LLM provider instances | 🟢 Decided | v1.0 | internal/llm/openaicompat, admin API, admin UI |
| ADR-033 | Premium OpenAI client split from compat client         | 🟢 Decided | v1.0 | internal/llm/openai, internal/llm/openaicompat, main.go |
| ADR-034 | Webhook secrets stored in encrypted DB column (scoped ADR-002 deviation) | 🟢 Decided | v1.0 | policies table, internal/policy, trigger/webhook_handler, frontend WebhookConfig |
| ADR-035 | DB-backed system settings for runtime configuration | 🟢 Decided | v1.0 | system_settings table, admin API, frontend /admin/system |
| ADR-036 | Centralized scheduler dispatcher                    | 🟢 Decided | v1.0 | internal/dispatcher (new), internal/trigger/scheduled.go, internal/trigger/poll.go, main.go |
| ADR-037 | Custom Prometheus registry in internal/infra/metrics (leaf package) | 🟢 Decided | v1.0 | internal/infra/metrics (new), all future instrumented packages |
| ADR-038 | Atomic run-state transitions with optimistic locking   | 🟢 Decided | v1.0 | runs.version column, RunStateMachine.Transition (tx), runstate.ErrTransitionConflict |
| ADR-039 | Per-server encrypted auth headers for authenticated MCP providers | 🟢 Decided | v1.0 | mcp_servers table, internal/mcp, internal/admin, gleipnirctl rotate-key |
| #611    | Remove claudecode agent runtime                        | 🟢 Decided | v1.0 | internal/agent/claudecode deleted; policies using provider: claude-code now fail validation |

---

## ADR-039: Per-server encrypted auth headers for authenticated MCP providers

**Status:** Decided
**Date:** 2026-04

### Context

Some MCP providers require authentication on every HTTP request. Composio, for example, requires an `x-api-key` header carrying a per-account API token. The `mcp_servers` table previously stored only `name` and `url` — there was no mechanism to attach authentication material to a server registration. Operators working around this would have to embed credentials in the URL (query parameters), which are visible in logs and in the Gleipnir UI.

Gleipnir already has a pattern for this problem: `internal/admin` provides an AES-256-GCM encrypt/decrypt helper (keyed from `GLEIPNIR_ENCRYPTION_KEY`) used for provider API keys and webhook secrets.

### Decision

**Storage:** A new `auth_headers_encrypted TEXT` column is added to `mcp_servers`. It stores a JSON array of `{name, value}` objects, encrypted with AES-256-GCM via the existing `internal/admin` helper. The column is nullable — absence means no configured auth headers.

**API surface (write-only values):** `POST /api/v1/mcp/servers` accepts an `auth_headers` field containing an array of `{key, value}` objects (plaintext, used only at creation time). `PUT /api/v1/mcp/servers/:id` updates `name` and `url` only — it does NOT touch `auth_headers_encrypted`. Auth headers are managed per-header via write-only endpoints that mirror ADR-034's webhook-secret pattern:

- `PUT  /api/v1/mcp/servers/:id/headers/:name` — set or replace one header (admin|operator). Body: `{"value": "string"}`. The comparison against stored names is case-insensitive; the submitted casing wins.
- `DELETE /api/v1/mcp/servers/:id/headers/:name` — remove one header (admin|operator). Idempotent: no-op if the header is absent. Deleting the last header sets the column to NULL.

`GET` responses return header *names* only (`auth_header_keys`); values are never included in any response. There is no sentinel and no preserve-vs-overwrite ambiguity because edits are scoped to a single header at a time. `MaskedHeaderValue` was considered (as a bulk-PUT sentinel) and rejected before merge.

**Header name validation:** Header names are validated with `golang.org/x/net/http/httpguts.ValidHeaderFieldName` (RFC 7230 token syntax), which rejects CR, LF, NUL, colon, and all non-token characters. A fixed reserved-name list (`Mcp-Session-Id`, `Content-Type`, `Accept`, `Content-Length`, `Host`) is additionally rejected — these headers are managed by the MCP client or the HTTP transport layer and must not be overridden.

**MCP client injection:** `internal/mcp` registry decrypts `auth_headers_encrypted` when loading a server and passes the plaintext headers to the HTTP client. Every outbound request to that MCP server includes the configured headers. `internal/mcp` imports `internal/admin` for decryption — this avoids forcing every `Registry` caller (HTTP handlers, CLI commands, poll trigger) to know about the encryption key and perform decryption themselves. The existing `internal/trigger` → `internal/admin` import provides precedent for a non-leaf package importing `internal/admin`.

**`POST /api/v1/mcp/servers/test`:** The test-connection endpoint accepts `auth_headers` inline. It injects the provided headers for the one-off connection test without persisting anything. This allows operators to verify a new server configuration (including auth) before committing it.

**`gleipnirctl rotate-key`:** The key rotation command re-encrypts `auth_headers_encrypted` in the same transaction as all other encrypted columns (`provider_api_key_encrypted`, `openai_compat_key_encrypted`, `webhook_secret_encrypted`). No additional rotation path is needed.

### Out of scope

- Per-policy or per-user credential scoping. All policies that grant tools from a given MCP server share the same auth headers. Scoped credentials require URL templating and/or a new `policy_mcp_overrides` join table — deferred to a follow-up issue.
- OAuth orchestration of the Composio account itself. Operators connect their OAuth integrations in the Composio dashboard; Gleipnir only holds the Composio API key, not downstream OAuth tokens.

### Consequences

- Operators can register any MCP server that requires a static API key or bearer token without embedding credentials in the URL.
- Auth header values are never returned over the API. An operator who loses their Composio API key must regenerate it at the source and update the Gleipnir server registration — Gleipnir provides no recovery path for the plaintext value.
- The trust expansion introduced by connecting to a hosted provider like Composio (where downstream OAuth tokens for Gmail, Slack, etc. reside with that provider) is operator-visible — documented in the Composio playbook (`docs/playbooks/composio/README.md`).
- `internal/mcp` now imports `internal/admin`. This is an intentional package boundary adjustment, consistent with the `internal/trigger` → `internal/admin` precedent.

---

## ADR-038: Atomic run-state transitions with optimistic locking

**Status:** Decided
**Date:** 2026-04

### Context

`RunStateMachine.Transition()` performed multiple DB writes sequentially without a wrapping transaction:
1. `UPDATE runs SET status = ...`
2. (Conditionally) `INSERT INTO approval_requests ...` or `INSERT INTO feedback_requests ...`

If step 2 failed, the run status was already changed in the DB with no rollback, leaving the run in an inconsistent state.

Additionally, there was no optimistic locking on the `runs` table. The state machine used an in-process mutex to guard its own `current` field, but two separate state machine instances (e.g. the agent goroutine and the timeout scanner) could both pass the `IsLegalTransition` check in memory and then both issue UPDATEs to the DB. The last write would win silently.

**Race scenario:** An approval times out (scanner transitions `waiting_for_approval → failed`) at the same millisecond the operator approves it (agent transitions `waiting_for_approval → running`). Both writes succeed; the final state is whichever UPDATE executes last in SQLite's WAL serialization order.

### Decision

**Transactions:** Wrap multi-step transitions in a DB transaction so that if `INSERT INTO approval_requests` fails, the `UPDATE runs` is rolled back.

**Optimistic locking (CAS):** Add a `version INTEGER NOT NULL DEFAULT 0` column to `runs`. Increment it on every status UPDATE:

```sql
UPDATE runs SET status = :status, version = version + 1
WHERE id = :id AND version = :expected_version
```

If `rows_affected == 0`, the transition was lost to a concurrent write — return `runstate.ErrTransitionConflict` so the caller can handle it explicitly.

`ErrTransitionConflict` lives in `internal/runstate` (not `internal/agent`) because both `agent` and `timeout` packages need it. `internal/db` cannot import `runstate` (would be a cycle), so `store.go` defines a local-package sentinel with the same string.

In-memory state (`sm.current`, `sm.version`) is updated ONLY after `tx.Commit()` succeeds, so a commit failure leaves the state machine consistent with the DB.

### Consequences

- Every transition is now durable-or-rolled-back; partial writes are impossible.
- Concurrent writers fail loudly (`ErrTransitionConflict`) instead of silently overwriting each other.
- Callers must handle the new error: timeout scanners treat it as a benign race (same as `ErrIllegalTransition`); the agent goroutine exits cleanly.
- Reviving a state machine for an existing run requires loading the current `version` from the DB and passing it via `WithInitialVersion`.

---

## ADR-036: Centralized scheduler dispatcher

**Status:** Decided
**Date:** 2026-04

### Context

Gleipnir's trigger subsystem has accumulated two parallel implementations of "do work at time T": `internal/trigger/scheduled.go` and `internal/trigger/poll.go`. Each owns its own long-running goroutines, its own mutex/map/waitgroup lifecycle bookkeeping, and its own reconciliation plumbing. #791 recently added a `PolicyNotifier` interface with `Notify` methods on both — further duplicating the "stay in sync with DB state" plumbing across both subsystems.

The duplication has concrete costs:

- Bug #790 ("Scheduler has no reconcile loop") existed because the pattern had to be reimplemented per trigger type. When Poller got a reconcile loop, Scheduler did not. Applying the Poller fix to Scheduler (via #791) required ~100 lines of near-identical bookkeeping in each.
- Goroutine count grows with configuration: one per active poll policy plus a reconcile goroutine; one per `fire_at` timestamp on every scheduled policy.
- Adding any new timed primitive (e.g. future denial-with-reason timeouts, retry schedulers, rate-limit budgets) would repeat the pattern a third time.
- Startup wiring, shutdown draining, and `rootCtx` handoff for long-lived `Notify` calls live independently in each subsystem.

### Decision

Scheduling is centralized behind a single `Dispatcher` interface with an in-memory implementation:

```go
type Dispatcher interface {
    Schedule(fireAt time.Time, kind string, payload any) int64
    Cancel(jobID int64)
    RegisterHandler(kind string, fn HandlerFn)
}

type HandlerFn func(ctx context.Context, payload any)
```

The `memoryDispatcher` is a leaf package that owns one min-heap keyed on `fireAt`, one goroutine that sleeps until the heap top, and a handler registry populated at startup. `Scheduler` and `Poller` are retired; their logic moves into handlers registered against the dispatcher by `kind` (`scheduled_fire`, `poll_tick`). Poll is modeled as self-rescheduling — each tick handler schedules the next tick after firing.

Source of truth for pending work remains the existing tables (`policies`, policy YAML). The heap is an in-memory index rebuilt on startup by scanning those tables. Policy save paths call `Schedule()` synchronously — no reconcile loop, no notify interface, no up-to-N-seconds latency. Handlers re-check policy status at fire time and drop the fire if the policy has been paused or deleted, keeping delete/pause paths ignorant of the dispatcher.

Scope is limited to the scheduler/poll subsystems. The approval and feedback timeout scanners (`internal/timeout/`) retain their scan-for-state-change pattern, which is a better fit for "has this pending request been resolved yet?" than a per-request timer. The agent-run goroutines in `internal/run/` and `internal/agent/` are unchanged — they hold real blocking LLM/tool I/O and should remain goroutine-per-run.

### Rejected alternatives

**DB-backed scheduled_jobs table.** A new `scheduled_jobs(id, kind, payload, run_at, taken_at, completed_at)` table with a polling dispatcher. Rejected because the information already lives in existing tables (`policies.fire_at`, `approval_requests.expires_at`, policy YAML intervals) — a jobs table would duplicate rather than consolidate. DB-polling queues also scale poorly (polling pressure, row-lock contention, awkward fencing semantics under concurrent writers) and do not provide a clean foundation for future multi-node HA.

**Full event-driven refactor.** Every agent step becomes an event on a shared bus consumed by a worker pool. Rejected because the pain motivating this change is scheduler sprawl, not agent execution. Agent goroutines hold legitimate blocking I/O; converting them to event-driven workers would require reconstituting provider-specific state (tool_use_id pairing, reasoning tokens, streaming cursor position) between every step, with no corresponding win at single-node scale. The dispatcher interface is designed to compose with an event-driven layer later if it is ever warranted — it does not preclude that future.

**Replicate the #791 notify pattern for every future timed primitive.** Keep the current structure and accept that each new timed subsystem pays the same ~100-line lifecycle tax. Rejected because the cost compounds: each new primitive adds a new reconcile loop, a new notify interface, new startup wiring, and a new category of "stale-state" bug class.

### Multi-node HA path

The `Dispatcher` interface is designed for substitution. When multi-node Gleipnir is pursued, a new implementation (`leaderOnlyDispatcher`, `raftDispatcher`, or an external-primitive-backed variant using NATS JetStream, etcd leases, or Temporal) implements the same interface without caller changes. Committing to a DB-backed queue now would accrue migration debt against whatever coordination primitive is chosen later; the in-memory choice keeps that decision deferred and cheap.

### Migration

1. Add `internal/dispatcher/` package with `memoryDispatcher`, `jobHeap`, fake-clock test scaffolding, and unit tests.
2. Migrate `scheduled.go`: register `scheduled_fire` handler, seed heap from `GetScheduledActivePolicies` on startup, call `Schedule()` from the policy save path, delete the `Scheduler` struct and its `PolicyNotifier` implementation. Closes #790.
3. Migrate `poll.go`: register `poll_tick` handler that reschedules itself, seed first tick per active poll policy on startup, delete the `Poller` struct, its reconcile loop, and its `PolicyNotifier` implementation.

Design detail, diagrams, and handler contracts live in [`docs/developer/dispatcher.md`](developer/dispatcher.md).

---

## ADR-037: Custom Prometheus registry in internal/infra/metrics (leaf package)

**Status:** Decided
**Date:** 2026-04

### Context

`github.com/prometheus/client_golang` registers collectors on a process-wide global default registry (`prometheus.DefaultRegisterer`) unless callers explicitly pass their own registry. A global registry couples every future instrumented package to init-order: if two packages register the same metric name, the second registration panics at startup and the cause can be hard to locate. It also leaks metric registrations across tests — a collector registered in one test binary persists for the lifetime of the process, causing `AlreadyRegisteredError` when a second test registers a same-named collector. The upcoming per-package instrumentation plan (spec `2026-04-09-metrics-and-logging-design`) needs one shared, explicit registry that all domain packages inject into, rather than a hidden global side channel.

### Decision

Introduce `internal/infra/metrics` as a leaf package that owns a package-private `*prometheus.Registry` (created with `prometheus.NewRegistry`, not `prometheus.NewPedanticRegistry`). The registry is initialized once in `init()` with the Go runtime collector (`collectors.NewGoCollector()`). Two exported accessors are provided:

- `Registry() *prometheus.Registry` — domain packages call `promauto.With(metrics.Registry())` to register their own collectors. The concrete type is returned (not the `Registerer` or `Gatherer` interface) so callers can use it as both without forcing separate accessors.
- `Handler() http.Handler` — returns `promhttp.HandlerFor(registry, promhttp.HandlerOpts{Registry: registry})` for mounting on the `/metrics` route in a follow-up PR.

Shared bucket presets (`BucketsFast` for MCP/DB latency, `BucketsSlow` for LLM/run duration) and label-key/enum constants (`LabelErrorType`, `ErrorTypeTimeout`, `DirectionInput`, etc.) also live in this package so the `gleipnir_` naming scheme and fixed `error_type` enum stay authoritative in one place.

### Rejected alternative

Using `prometheus.DefaultRegisterer` (the global default registry). Rejected because it couples every package to init-order, makes isolated testing awkward (registrations persist globally across tests), and obscures which registry the `/metrics` endpoint actually exposes. A custom registry makes the dependency explicit and traceable.

### Consequences

- Every future instrumented package imports `internal/infra/metrics` for the registry and shared constants. `internal/infra/metrics` imports no other internal package — it is a leaf package.
- The `/metrics` route and `GLEIPNIR_METRICS_ENABLED` / `GLEIPNIR_METRICS_PATH` env vars are added in a follow-up PR that mounts `metrics.Handler()` into the chi router.

---

## ADR-035: DB-backed system settings for runtime configuration

**Status:** Decided
**Date:** 2026-04

### Context

Gleipnir needs runtime-configurable settings that survive container restarts and image upgrades. The first such setting is `public_url` — the external URL where Gleipnir is accessible, used to construct full webhook URLs for display. Environment variables are unsuitable because they require container restart to change and cannot be edited from the UI.

### Decision

Runtime configuration is stored in the existing `system_settings` key/value table (TEXT PRIMARY KEY `key`, TEXT NOT NULL `value`, TEXT NOT NULL `updated_at`). Settings are managed via `GET/PUT /api/v1/admin/settings` (admin-only). A separate `GET /api/v1/config` endpoint exposes non-sensitive settings (currently `public_url`) to all authenticated users.

The `public_url` setting is validated on write: it must be an absolute URL with scheme and host. Trailing slashes are stripped. An empty value clears the setting via `DELETE` (not upsert — `value TEXT NOT NULL` allows empty strings, so storing `""` would be indistinguishable from "not set"). When unset, features that depend on it fall back gracefully (e.g., webhook URL display shows path-only).

### Rejected alternative

Environment variable (`GLEIPNIR_PUBLIC_URL`): rejected because it requires container restart, cannot be edited from the UI, and doesn't survive Docker image upgrades that reset env vars.

---

## ADR-034: Webhook secrets stored in encrypted DB column (scoped ADR-002 deviation)

**Status:** Decided
**Date:** 2026-04

### Context

ADR-002 established that all policy configuration lives in the `yaml` column, with only `name` and `trigger_type` as indexed columns. Webhook shared secrets are a category of data that violates this rule: the `yaml` column is returned wholesale by `GET /api/v1/policies/:id`, which means storing a plaintext secret there would expose it to any authenticated user regardless of role, and would also leak the secret into audit logs and exports.

### Decision

Webhook secrets are stored in a dedicated `webhook_secret_encrypted TEXT` column on the `policies` table, encrypted with AES-256-GCM using the key from `GLEIPNIR_ENCRYPTION_KEY`. This is an intentional, scoped deviation from ADR-002. YAML remains the source of truth for all other policy fields.

The `trigger.auth` field (`hmac | bearer | none`) **does** live in the YAML, because it is configuration (not a secret) and operators need to see and edit it in the policy editor. The encrypted secret value is never included in any policy GET response, SSE payload, or export.

### Rejected alternative

Redacting the secret from `GET /api/v1/policies/:id` response while keeping it in YAML: rejected because it creates a partial-round-trip problem — the field would silently disappear on re-save unless every edit path was made secret-aware. Storing it separately is cleaner and eliminates the surface area entirely.

### Auditor access posture

Auditors can read `trigger.auth` mode from the policy YAML (visible in `GET /api/v1/policies/:id`). They cannot call the reveal endpoint (`GET /policies/:id/webhook/secret`) or the rotate endpoint (`POST /policies/:id/webhook/rotate`), both of which require admin or operator role.

### Export / import consequences

Policy export (the YAML blob) does not include the secret. An exported policy imported to a new instance will have `trigger.auth: hmac` (or `bearer`) set but no secret — the operator must generate a new secret via the rotate endpoint. This is intentional: exporting a secret alongside a policy would undermine the encrypted-column separation.

### Legacy migration

Policies created before this change may have `webhook_secret:` in their YAML. A one-time eager startup migration (`policy.MigrateLegacyWebhookSecrets`) detects these rows, encrypts the secret into the new column, and removes the field from the stored YAML. The grandfathering rule: a policy that had a `webhook_secret` but no `auth` field defaults to `auth: hmac` (preserving original security posture). Only policies with neither field default to `auth: none`.

---

## Issue #611: Remove claudecode agent runtime

**Status:** Decided
**Date:** 2026-04
**Supersedes:** any reference to `internal/agent/claudecode/` or the `claude-code` provider

### Decision

The `internal/agent/claudecode/` subprocess runner has been deleted. The `claude-code` provider is no longer a supported policy provider. Policies that declare `model.provider: claude-code` fail at validation time with an actionable error message listing the supported LLM providers (anthropic, google, openai, openaicompat).

Existing policies stored in the database with `provider: claude-code` are not auto-migrated. They will produce a validation error on first load after deploy, prompting the operator to update the YAML. This follows ADR-001's philosophy of explicit operator action over silent behaviour changes.

`(c *Client) ServerURL()` in `internal/mcp/client.go` was also removed as it was only consumed by the deleted ClaudeCodeAgent.

---

## ADR-016: Real-time UI transport: SSE over WebSockets

**Status:** Decided
**Date:** 2026-03 (addendum 2026-04)

**Decision:** Server-Sent Events (SSE) is the real-time transport for pushing run state changes,
new approval requests, and reasoning steps from the Go backend to the React frontend.
Approve/reject and other mutations remain REST calls.

**Rejected alternative:** WebSockets. Full-duplex is not needed — all real-time messages
originate on the server. Client→server actions (approve, reject, feedback response) are
natural REST calls and do not require a persistent bidirectional channel.

**Reasoning:**

- **HA scaling path.** WebSockets require sticky sessions or a pub/sub broker to fan events
  across multiple instances. SSE connections are stateless HTTP — any instance can serve any
  client. Horizontal scaling requires only a message broker interface (in-process channels for
  v1, Redis Pub/Sub or NATS for HA), with no load balancer changes.
- **Reverse proxy compatibility.** SSE passes through nginx, API gateways, and CDN layers as
  ordinary HTTP. WebSockets require explicit `Upgrade` header support at every proxy layer —
  a deployment friction point for enterprise environments.
- **Native reconnection.** The browser `EventSource` API reconnects automatically after
  disconnection and supports `Last-Event-ID` for resuming a stream after a Gleipnir restart
  or instance failover. WebSocket reconnection requires explicit client-side logic.
- **Zero dependencies.** SSE is plain `text/event-stream` over an HTTP handler in Go.
  WebSockets require `gorilla/websocket` or equivalent.

**Implementation constraint:** The SSE broadcaster in Go must be written against an interface
(`EventBroadcaster`) rather than directly against an in-process channel. This makes swapping
in Redis Pub/Sub or NATS a seam, not a rewrite, when the HA tier is introduced.

**Events to stream:**
- `run.status_changed` — run transitions between states
- `run.step_added` — new reasoning step written
- `approval.created` — new approval request surfaced
- `approval.resolved` — approval decided or timed out
- `mcp.drift_detected` — tool registry change detected

**Consequence:** The Go SSE handler must flush each event immediately. Since the frontend is
now served directly by the Go HTTP server (ADR-006 revised), there is no nginx buffering layer
to contend with — the `http.Flusher` interface in the SSE handler is sufficient.

**Addendum (2026-04): Reconnection semantics**

Native `EventSource` cannot set request headers, so it cannot send `Last-Event-ID` on
reconnect. The frontend reconnection path is therefore implemented with `fetch` +
`ReadableStream` in the `useSSE` hook. Every event carries a monotonic `id:` field; on
reconnect the client sends `Last-Event-ID: <id>` and the server replays any buffered events
with a higher id.

Backoff schedule: 1s → 2s → 5s → 15s, held at 15s on further failures, reset to 1s on any
successful connect. The UI shows `reconnecting` for failures 1–4 and transitions to
`disconnected` only on the 5th consecutive failure (i.e. after the first 15s retry itself
fails). An idle watchdog aborts and reconnects the stream if no bytes (including the 15s
`: keepalive` heartbeats) arrive within 30 seconds — this recovers from silent TCP half-close
on mobile / VPN paths. This addendum does not supersede ADR-016; it documents the
client-side contract the Go handler already implements.

**Addendum (2026-04): Drop observability and buffer sizing**

When a subscriber's per-client channel buffer is full, the broadcaster drops the event for
that subscriber rather than blocking delivery to all other subscribers. This is a hard
guarantee, not a best-effort hint. The new counter `gleipnir_sse_events_dropped_total`
(no labels, matching the unlabelled style of `gleipnir_approval_timeouts_total`) is the
production signal for undersized buffers. A sustained non-zero rate means clients are
receiving events faster than they can drain their channel and the defaults should be raised.

The documented recovery path for a client that missed events is automatic. On reconnect the
frontend sends `Last-Event-ID` and the server's `Replay` method returns every buffered event
with a higher id. Per-route REST queries (`useRuns`, `useRunDetail`, `useRunSteps`, etc.)
invalidate on each SSE event via TanStack Query and act as a reconciliation layer when the
replay window is exceeded — the UI re-fetches current state and converges even if some events
were dropped and the ring buffer has already overwritten them.

Default buffer sizes were raised to 256 per-subscriber and 2048 ring (from 64 and 512
respectively). The original defaults were chosen before per-step event streaming; under load
they dropped too readily. These are code defaults, not env vars, to keep the operational
surface small. `WithChannelSize` and `WithRingSize` remain the escape hatch for tests and
future tuning without adding `GLEIPNIR_SSE_*` environment variables that have no concrete
per-deployment need today.

---

## ADR-017: Policy-level parameter scoping for MCP tools

**Status:** Decided
**Date:** 2026-03

**Decision:** Policy tool entries may declare a `params` block that restricts the allowed
values for specific input parameters. Gleipnir narrows the tool's input schema before
presenting it to the agent, and validates the agent's call against the declared constraints
before dispatch. The call itself is never modified — if it passes validation it reaches the
MCP server exactly as the agent sent it.

**Example:**
```yaml
sensors:
  - tool: kubectl.get_pods
    params:
      namespace: ["worker-01", "worker-02", "worker-03"]

actuators:
  - tool: kubectl.delete_pod
    approval: required
    params:
      namespace: ["worker-01", "worker-02", "worker-03"]
```

**Mechanics:**

- At run start, Gleipnir takes the MCP server's declared input schema for each tool and
  narrows any field listed in `params` to an enum of allowed values before registering
  the tool with the Claude API. The agent receives a tool whose schema only permits the
  declared values — it cannot construct a call outside the allowed set.
- For single-value params (`namespace: "worker-02"`), the field becomes a constant enum
  with one member. The agent has no meaningful choice but still sends the value explicitly.
- At call time, the runtime validates the agent's input against the scoped schema before
  dispatch. A call with a value outside the allowed set is rejected immediately with a
  descriptive error written to the run_steps audit log. The call never reaches the MCP server.
- The MCP server receives the call exactly as the agent constructed it — no injection,
  no merging, no transformation.

**Reasoning:**

The MCP server registry describes what tools exist and what they can do. The policy
describes what a specific agent is allowed to know and do for a specific job. These are
genuinely different concerns. The same tool should be scopeable differently across policies
without requiring separate MCP server registrations.

Enforcement at schema presentation time (before the agent's first message) is consistent
with ADR-001: the agent literally cannot construct an out-of-scope call because the
out-of-scope values do not exist in the schema it received. This is not prompt guidance —
it is a structural constraint on the tool description the agent reasons from.

**Rejected alternatives:**

- **Silent param injection:** Gleipnir merges locked values into the call after the agent
  sends it. Rejected because it creates invisible constraints — the agent may reason
  incorrectly about why it's getting the results it is. Invisible enforcement is harder
  to audit and debug.
- **Registry-level scoping:** Restricting tool parameters at the MCP server registry level.
  Rejected because it prevents the same tool from being used with different scopes in
  different policies.
- **Prompt-based restriction:** Telling the agent "only use namespace worker-02" in the
  system prompt. Rejected per ADR-001 — suggestions are not controls.

**Consequences:**

- **EPIC-002 (Policy Engine):** `params` is an optional map on any tool entry (sensor or
  actuator). Validator warns at save time if a param name doesn't appear in the tool's
  discovered input schema. Validator blocks at run start if a referenced tool is missing.
- **EPIC-003 (MCP Client):** Schema narrowing applied at run start before tool registration.
  Call validation applied before dispatch. Both paths must produce clear errors for the
  audit log.
- **EPIC-004 (BoundAgent Runtime):** Tool registration uses the narrowed schema. Validation
  errors written as `error` steps in run_steps, not swallowed.
- **Policy validator:** Warn if a tool in a policy accepts a parameter that is scoped on
  some tool entries but not others — the cross-tool bleed check.

---

## ADR-018: Capability snapshot as the first step of every run

**Status:** Decided
**Date:** 2026-03

**Decision:** Every run begins with a `capability_snapshot` step written to run_steps
before the agent's first message is sent. This step records the complete tool list exactly
as presented to the agent — tool names, capability roles, approval requirements, and the
narrowed input schemas including any `params` constraints declared in the policy.

**Reasoning:**

The capability snapshot is the primary diagnostic tool for understanding agent behaviour.
Any question of the form "why did / didn't the agent do X" starts here: did it have the
tool, what were its allowed parameter values, was approval required? Most diagnostic
questions are answerable from the snapshot alone without reading the full reasoning trace.

The snapshot is particularly critical for param-scoped policies (ADR-017). If an operator
asks "why didn't the agent touch worker-04", the answer is in the capability snapshot:
`namespace: worker-01 | worker-02 | worker-03`. Worker-04 was never in the agent's world.

The snapshot is written once, at step 0, before any agent interaction. It is immutable
for the lifetime of the run — it records what the agent was told, not what it did.

**Step type:**

`capability_snapshot` is added to the `type` enum in the `run_steps` table.

**Content JSON shape:**
```json
{
  "tools": [
    {
      "name": "kubectl.get_pods",
      "role": "sensor",
      "approval_required": false,
      "presented_schema": {
        "namespace": { "type": "enum", "values": ["worker-01", "worker-02", "worker-03"] },
        "label_selector": { "type": "string", "optional": true }
      }
    },
    {
      "name": "kubectl.delete_pod",
      "role": "actuator",
      "approval_required": true,
      "approval_timeout": "30m",
      "on_timeout": "reject",
      "presented_schema": {
        "namespace": { "type": "enum", "values": ["worker-01", "worker-02", "worker-03"] },
        "pod": { "type": "string" }
      }
    }
  ]
}
```

**UI rendering (Reasoning Timeline):**

The capability snapshot card sits at the bottom of the timeline (it is step 0, and the
timeline renders newest-first). It is collapsed by default. Its summary row reads
"capability snapshot — N tools". Expanding it shows a structured tool list: name, role
chip, approval badge if required, and the presented schema with enum constraints
highlighted. This makes the diagnostic flow immediate: operator opens timeline, scrolls
to bottom, expands snapshot, sees the agent's exact world at run start.

**Consequences:**

- **EPIC-001 (Data Model):** `capability_snapshot` added to the `run_steps` type enum.
  Content JSON schema documented above. No additional columns required.
- **EPIC-004 (BoundAgent Runtime):** Write the snapshot as step 0 before the first
  Claude API call. Token cost is 0 (no LLM involvement).
- **EPIC-007 (Frontend):** Reasoning timeline renders `capability_snapshot` card type.
  Collapsed by default. Always the last card in the list (oldest step, rendered at bottom
  in newest-first ordering). Never included in the filter chip counts — it is infrastructure,
  not agent reasoning.

## ADR-001: Hard capability enforcement at runtime, not prompt level

**Status:** Decided
**Date:** 2026-03

**Decision:** Capability controls are enforced by not registering disallowed MCP tools with the
BoundAgent for a given run. The agent literally cannot call a tool it hasn't been granted — it
doesn't exist in its tool list.

**Rejected alternative:** Prompt-based restrictions ("you are not allowed to delete anything").
These are suggestions, not controls. They can be reasoned around, forgotten in long contexts, or ignored.

**Consequence:** The MCP tool registry and capability tagging system are core infrastructure, not an afterthought.

**Affects epics:** EPIC-003 (tool registry enforcement), EPIC-004 (BoundAgent runtime)

**Implementation note:** The concrete runtime enforcement mechanism for ADR-001 is `ResolveForPolicy` in `internal/mcp/registry.go`. It performs a fail-fast check at run start: every tool reference in the policy's `sensors` and `actuators` lists is looked up in the registry DB. If any tool is not found, the run is aborted before the agent is started — the disallowed tool never reaches the agent's tool list.

---

## ADR-002: Policy-as-YAML is the primary configuration primitive

**Status:** Decided
**Date:** 2026-03

**Decision:** Policies are defined in YAML, stored in the database, and edited via the UI's inline
editor. The UI reads and writes YAML directly — not a separate data model.

**Reasoning:** YAML is GitOps-friendly and readable. Storing inline in the DB (not as files) avoids
volume mount complexity in Docker Compose deployments.

---

## ADR-003: SQLite for initial storage

**Status:** Decided
**Date:** 2026-03

**Decision:** SQLite for all storage: policies, MCP registry, run history, reasoning traces,
approval requests. WAL mode enabled. Audit writes serialized through a queue to handle concurrent runs.

**Reasoning:** Zero-ops, single file, ships in the Docker image. Sufficient for homelab scale.
Can migrate to Postgres later.

---

## ADR-004: MCP-native, HTTP transport first

**Status:** Decided
**Date:** 2026-03

**Decision:** All tools are MCP tools. HTTP transport is the initial supported transport.
Users run their own MCP server containers and register the HTTP URL in Gleipnir.

**Consequence:** Gleipnir needs an MCP HTTP client in Go. Tool capability tags are managed
in Gleipnir's registry, not in the MCP server itself (standard MCP has no concept of capability tags).

---

## ADR-005: Go + chi + sqlc for the backend

**Status:** Decided
**Date:** 2026-03

**Decision:** Go with the chi router and sqlc for type-safe DB queries. Official Anthropic Go SDK
for the Claude API.

**Reasoning:** Go's concurrency model is well-suited for managing concurrent agent runs as goroutines
with context-based cancellation. Single binary deployment. chi is stdlib-aligned and minimal.
sqlc keeps the code close to SQL without an ORM.

---

## ADR-006: React frontend, embedded in Go binary

**Status:** Decided (revised)
**Date:** 2026-03

**Decision:** React + TypeScript app (Vite build) is embedded directly into the Go binary via
`go:embed` and served by the Go HTTP server. The Docker build uses a multi-stage Dockerfile:
Node builds `frontend/dist/`, then the Go stage copies it in before `go build` so the embed
directive captures it. nginx and a separate frontend container are eliminated. YAML editor uses
CodeMirror 6 (`@codemirror/lang-yaml`). Response envelope: `{ data: T }` for success,
`{ error: string, detail?: string }` for failure.

**SPA routing:** The Go server registers a catch-all `/*` route (`frontend.NewSPAHandler`) that
serves static assets directly and falls back to `index.html` for unknown paths, enabling
client-side routing.

**Caching:** Assets under `assets/` (Vite's hashed filenames) are served with
`Cache-Control: public, max-age=31536000, immutable`. `index.html` is served with `no-cache`.

**Design system:** IBM Plex Sans (body) + IBM Plex Mono (code/values). Dark theme with layered
backgrounds (`#0F1117` → `#131720` → `#1E2330`). Semantic colors: blue (sensors/running),
orange (actuators), amber (approvals), green (success), red (errors), purple (feedback/interrupted),
teal (poll). Full design token reference in `docs/Frontend_Roadmap.md`.

**Design reference:** Design tokens and visual language are defined in `frontend/src/tokens.css` and documented in `frontend/CLAUDE.md`.

**Reasoning:** Eliminates the nginx container, reducing the deployment footprint to a single
container. The Go binary becomes the sole deliverable — simpler ops for homelab deployments.
CodeMirror 6 chosen over Monaco for bundle size (~30KB vs ~2MB).

**Related:** ADR-016 (SSE), ADR-019 (dual-mode editor), ADR-020 (folders), ADR-021 (discovery diffs).

---

## ADR-007: BoundAgent model with Sensor / Actuator / Feedback roles

**Status:** Decided
**Date:** 2026-03

**Decision:** Every agent run operates as a BoundAgent with three semantically distinct tool
categories: sensors (read-only, called freely), actuators (world-affecting, optionally approval-gated),
and feedback (communication channel for human-in-the-loop).

**Reasoning:** The sensor/actuator/feedback model mirrors how a good human operator behaves —
observe, reason, then act or ask. Encoding this into the capability structure makes agent behavior
more predictable and auditable. The feedback channel as a first-class primitive (not just a
notification) enables genuine human-in-the-loop workflows.

**Consequence:** The policy schema, runtime interceptor, and UI all need to understand these three
roles distinctly.

---

## ADR-008: Two approval modes — agent-initiated and policy-gated

**Status:** Decided
**Date:** 2026-03

**Decision:** Support two approval modes simultaneously:

- **Agent-initiated:** the agent voluntarily uses the feedback tool when uncertain. Encouraged via
  system prompt, not enforced by the runtime.
- **Policy-gated:** certain actuators are configured with `approval: required`. The runtime
  intercepts the tool call before execution, fires the feedback channel, and suspends the run
  regardless of the agent's reasoning.

**Reasoning:** Agent judgment is valuable but not sufficient for high-stakes actions.
Policy-gated approval provides a hard guarantee that certain actions will always involve a human,
independent of model behavior.

---

## ADR-009: Feedback channel resolves policy-first, then system fallback

**Status:** Decided
**Date:** 2026-03

**Decision:** Each policy can define its own feedback channel config. If not set, Gleipnir falls
back to a system-level feedback config. The resolution order is: policy → system.

**Reasoning:** Allows a sensible default (e.g. a general Slack channel) while letting critical
policies route to dedicated channels or escalation paths.

---

## ADR-010: Project name is Gleipnir

**Status:** Decided
**Date:** 2026-03

**Decision:** The project is named Gleipnir, after the Norse mythological binding that held Fenrir.
Smooth as silk, stronger than iron, invisible in its constraint.

---

## ADR-019: Dual-mode policy editor (form + YAML)

**Status:** Decided
**Date:** 2026-03

**Decision:** The policy editor provides two modes toggled by a Form/YAML switch. Both modes
edit the same underlying YAML string. The form view parses YAML into structured fields (name,
description, folder, trigger, capabilities with tool picker, task instructions, limits,
concurrency). The YAML view is a CodeMirror 6 editor with syntax highlighting and validation.
Switching modes syncs data bidirectionally.

**Reasoning:** Raw YAML editing is powerful for operators who know the schema, but a form view
with a tool picker dramatically lowers the barrier for creating and editing policies. The
dual-mode approach serves both audiences without maintaining two data models — YAML remains
the single source of truth (ADR-002), and the form is a structured view into it.

**Consequence:** The frontend must include YAML parse/serialize logic. The form view requires
`GET /api/v1/mcp/servers` and tool list endpoints to populate the tool picker.

---

## ADR-020: Policy folders for UI grouping

**Status:** Decided
**Date:** 2026-03

**Decision:** Policies have an optional `folder` field in their YAML (default: "Ungrouped").
The dashboard groups policies into collapsible folder rows. Folders are purely cosmetic
organizational labels — they have no effect on trigger routing, runtime behaviour, or
access control.

**Reasoning:** As the number of policies grows, a flat list becomes hard to scan. Folders
provide lightweight organization without introducing a separate entity in the data model.
Storing folder as a YAML field (not a DB column) keeps the schema simple and consistent
with ADR-002 (policy-as-YAML). The dashboard derives folder groupings at read time.

**Rejected alternative:** Folders as a separate DB table with a foreign key on policies.
Rejected because folder membership has no runtime semantics — it's a UI-only concern and
doesn't justify a data model change.

---

## ADR-021: MCP discovery diffs

**Status:** Decided
**Date:** 2026-03

**Decision:** When `POST /api/v1/mcp/servers/:id/discover` is called, the response includes
a diff showing tools added, removed, and modified since the last discovery. The frontend
renders this as a visual diff with accept/assign actions. This is manual, operator-initiated
re-discovery — not automatic drift detection.

**Reasoning:** MCP servers evolve over time. When an operator updates an MCP server container
and re-discovers, they need to see what changed and assign roles to new tools. Showing a diff
is far more useful than silently updating the tool list. It also surfaces affected policies
(those referencing removed or modified tools) so the operator can update them.

**Consequence:** The discovery endpoint must compare the new tool list against the existing
registry and return a structured diff. Added tools need role assignment before they can be
used in policies.

---

## ADR-022: Transport-level fake for Anthropic API in tests

**Status:** Deferred (tracked: #78)
**Date:** 2026-03

**Decision:** Test infrastructure that needs to avoid real Anthropic API calls should inject
a fake `http.RoundTripper` into the `anthropic.Client` rather than bypassing the SDK via an
interface seam in production types.

**Rejected alternative:** `MessagesOverride MessagesAPI` field on `agent.Config`. This is a
test concern embedded in a production struct — production code should not be modified to
accommodate tests. Superseded by the `AgentFactory` pattern which already removed the seam
from `WebhookHandler`; `agent.Config.MessagesOverride` is the remaining field to eliminate.

**Consequence:** `agent.Config.MessagesOverride` and `integrationFakeMessages` to be removed
when the transport fake is implemented. `agent_test.go` and `integration_test.go` both move
to the transport fake.

---

## ADR-023: Per-policy model selection

**Status:** Decided
**Date:** 2026-03

**Decision:** Policies may declare an optional `agent.model` field selecting which Claude model
the agent uses. If omitted, the default is `claude-sonnet-4-6`. The field is validated at save
time against a local allowlist of three known model IDs, with an additional blocking API-level
check via `client.Models.Get`. The selected model is recorded in the capability snapshot
(alongside the tool list) so every run's audit trail captures the exact model used.

**Supported models:** `claude-opus-4-6`, `claude-sonnet-4-6`, `claude-haiku-4-5-20251001`.

**Rejected alternative:** Storing a system-wide default model in server config. Per-policy
selection gives operators the ability to match model capability to task complexity without
centralizing the decision.

**Consequences:**
- `internal/model.AgentConfig` gains a `Model string` field.
- `internal/policy` gains a `ModelValidator` interface and `AnthropicModelValidator` implementation.
- `internal/policy.NewService` signature updated to accept `ModelValidator` as a third argument.
- `internal/agent`: `MessageNewParams.Model` uses `anthropic.Model(a.policy.Agent.Model)` instead of
  the hardcoded `anthropic.ModelClaudeSonnet4_6` constant.
- Capability snapshot content shape changes from `[]GrantedTool` to `{model string, tools []GrantedTool}`.
  Frontend handles both shapes for backward compatibility with snapshots written before this change.

---

## ADR-024: Webhook HMAC-SHA256 signature verification

**Status:** Decided
**Date:** 2026-03

**Decision:** Webhook policies may declare an optional `trigger.webhook_secret` field (minimum 32
bytes). When set, every incoming `POST /api/v1/webhooks/{policyID}` request must include an
`X-Gleipnir-Signature: sha256=<hex>` header. The signature is the HMAC-SHA256 of the raw
request body using the configured secret. Comparison is timing-safe (`hmac.Equal`).

**Backward compatibility:** Policies without `webhook_secret` continue to accept requests with no
signature header (open webhook behaviour). Setting a secret does not break existing callers that
haven't yet been updated — until the operator sets the secret, the endpoint remains open.

**Response codes:**
- Secret configured, no header → 401 Unauthorized
- Secret configured, wrong signature → 403 Forbidden
- Secret configured, valid signature → proceed normally
- No secret configured → proceed normally (no header required)

**Rate limiting:** The webhook route is additionally protected by a per-process concurrency
throttle of 10 in-flight requests (`chi/middleware.Throttle`). This is applied only to the
webhook route, not globally.

**Secret length:** Minimum 32 bytes enforced by the policy validator. Shorter secrets are
rejected at save time with a clear error message.

**Secret storage:** `webhook_secret` is stored in the policy YAML blob (ADR-002). The
`TriggerConfig.WebhookSecret` field is tagged `json:"-"` to prevent the secret from appearing
in SSE events, run steps, or any JSON serialization of the config.

**Rejected alternative:** A shared global webhook signing key. Per-policy keys allow operators
to rotate secrets for individual integrations without affecting others.

---

## ADR-015: Policy Concurrency Model

**Status:** Decided
**Date:** 2026-03

**Decision:** v1.0 supports two concurrency modes, configured per policy in the `concurrency`
block of the policy YAML:

- **Skip** — if a run for this policy is already active (status `pending` or `running` or
  `waiting_for_approval`), the incoming trigger is dropped. The webhook still returns 202
  Accepted, but no run is created. The response body indicates the trigger was skipped and
  includes the ID of the currently active run.
- **Queue** — if a run is already active, the incoming trigger payload is held in a
  per-policy queue. When the active run reaches a terminal state (`complete`, `failed`,
  `interrupted`), the next queued payload is dequeued and a new run is created from it.
  Queue depth is bounded (default: 10 entries); payloads arriving when the queue is full
  are dropped with a 429 response.

`skip` is the default if no `concurrency` block is specified.

**Deferred to v1.1:** `parallel` (allow N concurrent runs up to a configured limit) and
`replace` (cancel the active run and immediately start a new one from the incoming trigger).
Both are architecturally compatible with the skip/queue implementation — they share the same
active-run detection path and require only additional branch handling.

**Policy YAML shape:**
```yaml
concurrency:
  mode: skip | queue
  queue_depth: 10    # only meaningful when mode is queue
```

---

## ADR-026: Model-Agnostic Design (Multi-Provider) — Revised

**Status:** Decided (revised 2026-03)
**Date:** 2026-03
**Supersedes:** Original ADR-026 (Model-Agnostic Design)

**Decision:** The LLM client is abstracted behind an `LLMClient` interface with three methods:
`CreateMessage` (stateless request/response translator), `StreamMessage` (returns a channel of
response chunks), and `ValidateOptions` (validates provider-specific policy options at save time).

The interface is a **stateless translator** — one request in, one API call, one response out. No
memory of previous calls. No decisions. The `BoundAgent` is the orchestrator: it owns the
conversation loop, conversation history, tool call routing, approval interception, audit trail,
and loop termination.

**v1.0 ships two providers:** Anthropic (Claude) via `anthropic-sdk-go` and Google (Gemini) via
`google.golang.org/genai`.

**Core types:** `MessageRequest` carries system prompt, full conversation history (provider-neutral
`ConversationTurn` slices), tool definitions (MCP-native JSON Schema), and optional `ProviderHints`.
`MessageResponse` returns ordered `ContentBlock` slices (text + tool calls interleaved), `StopReason`,
and optional `TokenUsage`. `MessageChunk` supports streaming with a `Done` flag and error channel.

**Provider hints:** Typed, provider-specific config via `ProviderHints` struct with `*AnthropicHints`
and `*GoogleHints` fields. Anthropic hints include `EnablePromptCaching` and `MaxTokens`. Google
hints include `EnableGrounding` and `ThinkingLevel`. All fields are pointers; nil means use default.

**Policy YAML model section:**
```yaml
model:
  provider: anthropic
  name: claude-sonnet-4-20250514
  options:
    enable_prompt_caching: true
```

The `provider` field selects the `LLMClient` implementation. The `name` field is the model identifier.
The `options` map is translated into `ProviderHints` at parse time — unknown options are validation
errors. A policy omitting `model` entirely uses the system default (configurable via env var,
defaulting to `anthropic/claude-sonnet-4-20250514`).

**Boundary of responsibilities:**

*BoundAgent owns:* conversation state (full history in neutral format, passed on every call), loop
termination (max turns, max tokens, timeout), tool call routing (MCP registry dispatch, parameter
validation per ADR-017, approval interception per ADR-028/029), parallel tool call batching, audit
trail, error handling, and conversation structure discipline (strictly alternating turns).

*LLMClient implementations own:* SDK interaction and auth, schema translation (MCP JSON Schema →
provider-native tool format), conversation format translation (roles and content blocks), response
normalization, tool call ID guarantees (synthetic UUIDs when provider returns empty IDs), error
result translation (`IsError` → provider convention), error mapping (rate limits, auth failures),
provider hints application, and option validation.

**Validation wiring:** A provider registry (keyed by name string) is created at startup holding all
`LLMClient` implementations. The policy validator receives this registry via DI and calls
`ValidateOptions` at save time — the policy package never imports provider SDKs.

**Package structure:** `internal/llm` contains the interface and shared types. `internal/llm/anthropic`
and `internal/llm/google` contain the two implementations. `internal/agent` imports `internal/llm`
for the interface; it never imports provider SDKs directly.

**Rejected alternatives:**
- Per-provider BoundAgent implementations — duplicates 5-10x more orchestration logic than it saves
- Neutral ToolDef intermediate struct — premature with two providers
- Stateful interface with internal conversation management — loses audit visibility
- Single-method interface (no streaming) — adding methods later is breaking
- CountTokens method — deferred to v1.1
- Limits in the interface — loop control is a BoundAgent responsibility

**Consequences:**
- `internal/llm` package created with interface, shared types, and two implementations
- `internal/agent` imports `internal/llm`, never provider SDKs
- Provider registry created at startup, injected into policy validator and trigger engine
- Policy parser validates `model` section including provider options via `ValidateOptions` at save time
- Audit trail records `provider` and `model_name` on every run record
- Capability snapshot (ADR-018) records tools in MCP-native `ToolDefinition` format
- Adding a new provider requires: implementing `LLMClient`, adding a `ProviderHints` field, registering in the registry — no BoundAgent changes

**Amendment (2026-04):** `ThinkingBlock` now uses opaque `ProviderState json.RawMessage` instead
of named provider-specific fields (`Signature`, `RedactedData`, `EncryptedContent`, `ID`). Each
provider package that has round-trip state defines its own unexported state struct and
marshal/unmarshal helpers; the shared interface carries only `Provider`, `Text`, `Redacted`, and
`ProviderState`.

Rationale: named fields created a lowest-common-denominator leak that grew with each new provider.
Opaque bytes scale to additional providers without touching the shared interface.

Per-provider adoption (not mandated uniformly):
- `internal/llm/anthropic`: defines `anthropicThinkingState{Signature, RedactedData}`. Round-trips
  via signature (non-redacted) or redacted-data (redacted blocks).
- `internal/llm/openai`: defines `openaiThinkingState{ID, EncryptedContent}`. Round-trips via the
  Responses API reasoning item ID and encrypted content.
- `internal/llm/google`: has no `ThinkingBlock` round-trip state today (its thought signature lives
  on `ToolCallBlock.ProviderMetadata["google.thought_signature"]`, out of scope). No state struct;
  its `ThinkingBlock` constructions compile unchanged.
- `internal/llm/openaicompat`: drops thinking blocks entirely. No state struct.

What does NOT change:
- `ProviderHints any` — typed-per-provider; request-time config where caller ergonomics favor a
  typed interface over opaque bytes.
- `ToolCallBlock.ProviderMetadata map[string][]byte` — already opaque bytes; map shape lets
  independent keys coexist (Google uses one key).

Cross-provider semantics: a block whose `Provider` does not match the current provider (empty or
mismatched) is silently skipped (Debug log). Empty `ProviderState` (nil or len 0) is also skipped
— treated as text-only with no round-trip data. Malformed `ProviderState` JSON returns an error
and the agent fails the run with a wrapped message — do not silently drop continuity.

Destructive migration: no DB schema change (`ThinkingBlock` provider-specific fields were never
persisted — the audit writer records only `{text, redacted}`). Fresh installs only for in-flight
conversations.

---

## ADR-028: Tool Risk Classification Model

**Status:** Decided
**Date:** 2026-03

**Decision:** Tool risk is expressed exclusively via per-tool `approval` configuration in the
policy YAML. There is no risk level abstraction (safe / elevated / critical), no tag system,
and no category-level default behavior. Every tool's approval requirement is stated explicitly
by the policy author at the point of use.

**Policy YAML shape:**
```yaml
tools:
  - tool: kubectl.get_pods
    params:
      namespace: ["worker-01", "worker-02"]

  - tool: kubectl.delete_pod
    approval: required
    params:
      namespace: ["worker-01", "worker-02"]

  - tool: mealie.search_recipes
    # no approval field — defaults to not required
```

The `approval` field on a tool entry accepts:
- `required` — the tool call is intercepted before execution; an operator must approve
- absent / omitted — no approval gate; the tool executes immediately

**Deferred:** Risk level labels (safe / elevated / critical) as optional metadata for UI
grouping and default-approval inference. If introduced in a later version, they will be
additive — the per-tool `approval` field remains the runtime primitive and any risk label
would only influence the form editor's defaults, never override an explicit per-tool setting.

**Reasoning:** The sensor/actuator distinction (original ADR-007) provided implicit risk
classification — sensors were implicitly safe, actuators were implicitly risky. With that
distinction removed, the temptation is to replace it with an explicit risk taxonomy. This
adds complexity at both the schema and runtime layers without providing meaningful benefit
for v1.0: the policy author already knows which tools are dangerous in their environment,
and making that judgment explicit in the policy is clearer than inferring it from a category.
A `kubectl.get_pods` call is safe in most contexts; in a policy with write-access to a
production cluster it may warrant approval. Only the policy author can make that call.

**Rejected alternatives:**
- Risk levels with runtime effect: adds a layer of indirection between what the YAML says
  and what the runtime does. Hard to reason about, harder to audit.
- Tags with policy rules keyed off them: significant schema complexity for v1.0 with no
  clear benefit over per-tool config.

**Consequence:** The policy schema `tools` entries have two fields beyond the tool reference:
`approval` (optional, `required` or absent) and `params` (optional, see ADR-017). No
additional fields or tables are needed. The runtime approval interceptor in `BoundAgent`
checks the per-tool approval flag directly from the parsed policy — no lookup into a
risk registry.

---

## ADR-029: Approval State Machine (v1.0 Minimal)

**Status:** Decided
**Date:** 2026-03

**Decision:** The v1.0 approval gate is a two-outcome gate: approve or deny. No reason field,
no agent feedback path, no per-tool timeout configuration.

**Approve path:**
1. `BoundAgent` intercepts the tool call, sets run status to `waiting_for_approval`, writes
   an `approval_request` step to the audit trail.
2. The SSE stream emits `approval.created` — the UI surfaces the request to any user holding
   the Approver role.
3. The operator clicks Approve in the UI, which calls `POST /api/v1/runs/:run_id/approval`
   with `{"decision": "approved"}`.
4. The approval decision is written as an `approval_decision` step in the audit trail.
5. `BoundAgent` unblocks, calls the MCP server, returns the result to Claude, sets run status
   back to `running`.

**Deny path:**
1. Same interception and notification as the approve path.
2. The operator clicks Deny.
3. The decision is written as an `approval_decision` step with `outcome: denied`.
4. `BoundAgent` unblocks, sets run status to `failed`, writes an `error` step with a
   structured failure record indicating which tool was denied and at which step.
5. The run terminates. Claude is not informed — the run simply ends.

**Timeout behavior:** A fixed global timeout applies to all approval gates (default: 30
minutes, configurable via environment variable at the instance level). On timeout, the
outcome is `denied` — the same path as an explicit denial. No auto-approve option in v1.0.

**Deferred to v1.1:**
- Denial with reason: operator provides a reason string; the reason is fed back to Claude
  as a structured tool result and the run continues rather than terminates.
- Denial hard-stop vs denial-with-reason as distinct outcomes (the full ADR-029 state
  machine).
- Per-tool timeout duration and per-tool timeout outcome (auto-approve vs auto-deny).
- Timeout with reason (auto-deny and inject a canned reason into the agent context).

These are additive changes. The approve/deny channel between the HTTP handler and
`BoundAgent` is designed as a typed struct (`ApprovalDecision{Outcome, Reason}`) from day
one — even though `Reason` is unused in v1.0, the channel shape does not need to change
when denial-with-reason is added.

**Reasoning:** The full approval state machine (PAT-005) is one of Gleipnir's strongest
product differentiators. It is deliberately deferred — not because it is unimportant but
because shipping a minimal gate first keeps the v1.0 surface area manageable and ensures
the audit trail, SSE notification, and UI approval flow are solid before adding the
complexity of agent-adaptive denial handling.

**Consequence:** `ApprovalDecision` struct carries `Outcome` (approved / denied / timeout)
and `Reason` (string, unused in v1.0 but present for forward compatibility). The
`approval_decision` step content records `outcome` and `tool_name`. A `run_approvals` table
(or equivalent column on `run_steps`) records the wall-clock time between `approval_request`
and `approval_decision` for future approval analytics. The global timeout is implemented as
a `time.After` in the `BoundAgent` approval wait loop.

---

## ADR-030: UI abstracts over tool transport — "Tools" page is protocol-agnostic

**Status:** Decided
**Date:** 2026-03

**Decision:** The frontend uses "Tools" as the page name, navigation label, and route (`/tools`).
Tool providers are called "sources" in all user-visible text. The backend API routes remain
`/api/v1/mcp/servers` and `/api/v1/mcp/tools` — unchanged. A redirect from `/mcp` to `/tools`
is in place for backward compatibility with any bookmarked URLs.

**Reasoning:** MCP is an implementation detail. Users care about what tools their agents can
use, not which transport protocol delivers them. Surfacing "MCP" in the UI would couple the
user's mental model to a specific protocol, making it harder to add non-MCP tool sources in
the future without a disruptive rename.

**Rejected alternative:** Keeping "MCP Servers" as the UI label. Rejected because it leaks an
implementation detail into the user interface and would require a UI rename when additional
transport types are supported.

**Consequence:** Component directories retain `MCPPage/` names as an internal detail — not
user-facing. Hook names (`useMcpServers`, `useAddMcpServer`, etc.) are unchanged. All
user-visible text uses "Tools" and "source" vocabulary. Backend API routes are not affected.

---

## ADR-031: Native feedback as a Gleipnir runtime primitive

**Status:** Decided
**Date:** 2026-04
**Supersedes (partially):** ADR-007 (sensor/actuator/feedback role model), ADR-008 (two approval modes), ADR-009 (feedback channel resolution)

### Background

The original design (ADR-007, ADR-008, ADR-009) treated feedback as an MCP tool tagged with `capability_role = feedback`. At runtime, Gleipnir would call the external MCP server, which returned a meaningless `{"status": "pending"}` response. The runtime then wrote three audit steps — `tool_call`, `tool_result`, `feedback_request` — for what is conceptually a single action: pausing the run and asking a human. This conflates notification transport (calling an MCP server) with feedback collection (pausing until a human responds).

Approval gating (ADR-029) is already a runtime primitive: the `BoundAgent` intercepts a tool call before MCP dispatch, pauses the run, and waits for a binary approve/deny decision from the operator. Feedback should follow the same architectural pattern, replacing MCP dispatch with a runtime pause that waits for freeform text.

### Decision

**1. Feedback is a runtime primitive, not an MCP concept.**

The runtime injects a synthetic `gleipnir.ask_operator` tool into the agent's tool list at run start (when feedback is enabled). This tool is never dispatched to an MCP server. When the agent calls it, `BoundAgent` intercepts the call — exactly as it intercepts approval-gated tools — pauses the run (`waiting_for_feedback`), and blocks on a `feedbackCh <-chan string` until the operator responds. The agent sees `gleipnir.ask_operator` as a normal tool with a defined input schema; it has no knowledge that the tool is synthetic.

Both agent implementations (`internal/agent/agent.go` and `internal/agent/claudecode/agent.go`) use the `feedbackCh` channel pattern for blocking on operator response. (Note: `internal/agent/claudecode/` was removed in issue #611; only `internal/agent/agent.go` remains.)

**2. MCP tools are always tools.**

The `capability_role` column has been fully removed from the `mcp_tools` table. All MCP tools discovered from external servers are tools without any role distinction. The `CapabilityRole` type, `CapabilityRoleTool`/`CapabilityRoleFeedback` constants, and `MCPTool.CapabilityRole` field have been removed from `internal/model`. A runtime migration (`migrateDropCapabilityRole`) recreates the table without the column for existing databases.

**3. Notification is orthogonal.**

When the runtime creates a feedback request, the `notify` package dispatches outbound alerts (SSE event to the UI, and future Slack/webhook callbacks). The agent does not know about notification transport. This decouples the feedback collection mechanism (synthetic tool + runtime pause) from the notification delivery mechanism (notify package).

**4. Response ingress is pluggable.**

The current ingress channel is the UI (`POST /api/v1/runs/:run_id/feedback`). Future channels — Slack callbacks, email reply parsing — converge to the same API endpoint or an internal resolution interface. The `BoundAgent` blocks on `feedbackCh <-chan string` regardless of which ingress source delivers the response.

**5. Feedback is synchronous from the agent's perspective.**

The run pauses (`waiting_for_feedback`) until the operator responds or the run's context is cancelled. In v1.0 there is no per-feedback timeout: the default is no timeout, consistent with the current `waitForFeedback` implementation. The policy schema supports a `timeout` field (see below) but v1.0 implementation defers enforcement of it. When timeout enforcement is added, expiry resolves the feedback request with a canned "no response received" message and the run continues.

**Note on two modes:** The behavioral distinction between voluntary feedback and policy-gated approval is preserved. What changes is the mechanism, not the concept. Agent-initiated feedback is simply the agent calling `gleipnir.ask_operator`; policy-gated approval remains the runtime interceptor checking `approval: required` flags on MCP tool calls. These are two separate paths through `BoundAgent`.

### `gleipnir.ask_operator` tool contract

The `gleipnir.` prefix signals that this is a runtime-provided tool, not an MCP server tool. Its input schema:

```json
{
  "type": "object",
  "properties": {
    "message": {
      "type": "string",
      "description": "The question or information to present to the human operator."
    }
  },
  "required": ["message"]
}
```

The tool returns a single string: the operator's freeform text response. It appears in the capability snapshot (ADR-018) with no `approval_required` flag and no `presented_schema` narrowing.

### Policy schema change — `capabilities.feedback`

`capabilities.feedback` changes from a list of MCP tool references (the previous `feedback: []` list) to an optional configuration block:

```yaml
capabilities:
  feedback:
    enabled: true   # optional; default: true
    timeout: 30m    # optional; Go duration string; default: no timeout (v1.0 defers enforcement)
```

When `feedback.enabled` is true, or when the block is omitted entirely (which defaults to enabled), the runtime injects `gleipnir.ask_operator` into the agent's tool list. When `feedback.enabled` is false, no feedback tool is available — this supports fully autonomous cron policies that should have no feedback channel.

The `timeout` field, if set, applies a deadline to each individual feedback request, not to the whole run. The schema supports the field now so existing policies do not need to be migrated when timeout enforcement is implemented.

### Reasoning

- The current approach conflates notification transport with feedback collection. The MCP call returns `{"status": "pending"}`, and the run writes three steps (`tool_call`, `tool_result`, `feedback_request`) for what is conceptually one action.
- Approval gating is already a runtime primitive that intercepts before MCP dispatch. Feedback follows the same pattern but collects freeform text rather than a binary decision. Consistency between the two mechanisms reduces implementation surface area.
- Removing the `capability_role` distinction simplifies the MCP registry. Tools are tools. The feedback channel is orthogonal to tool transport.

### Rejected alternatives

- **Keep feedback as an MCP tool with special handling.** Rejected because it conflates transport with collection, produces confusing triple-rendered audit steps, and requires MCP server cooperation (returning `{"status": "pending"}`) for what is entirely a Gleipnir runtime concern.
- **Make feedback a system prompt instruction only.** Rejected per ADR-001 — prompt-based restrictions are not controls. The agent must be able to pause a run deterministically, not just because the system prompt suggests it.
- **Auto-inject feedback into all runs without policy opt-out.** Rejected because some policies (fully autonomous cron jobs) should not expose a feedback channel at all.

### Consequences

- `internal/model`: `CapabilityRole` type and all associated constants (`CapabilityRoleTool`, `CapabilityRoleFeedback`) removed. `GrantedTool.Role` and `MCPTool.CapabilityRole` fields removed. The `CapabilitiesConfig.Feedback` field changes from `[]string` to a `FeedbackConfig` struct (`Enabled bool`, `Timeout duration`).
- `internal/agent`: Both `BoundAgent` implementations inject `gleipnir.ask_operator` as a synthetic tool at run start when feedback is enabled. The `dispatchToolCall` method intercepts calls to `gleipnir.ask_operator` before MCP dispatch, the same way it intercepts approval-gated tools. The existing `waitForFeedback` method is reused with the `message` field from the tool input.
- `internal/mcp`: No changes to the MCP client or registry. Feedback is no longer an MCP concept.
- `internal/policy`: Parser updated to handle the new `capabilities.feedback` config block shape. Prompt generator updated to remove feedback-role tool listing logic.
- `internal/notify`: Remains the outbound notification dispatch point. Receives a `feedback.created` event and fans out to configured channels.
- `schemas/policy.yaml`: Updated to reflect the new `capabilities.feedback` block shape.
- Capability snapshot (ADR-018): `gleipnir.ask_operator` appears in the snapshot tool list with a synthetic marker.
- `internal/db`: The `feedback_requests` table is unchanged — it already stores the feedback lifecycle independently of MCP.
- ADR-007 is partially superseded: the sensor/actuator/feedback three-role model collapses to tools (with optional approval) plus the runtime feedback primitive.
- ADR-008 is partially superseded: "agent-initiated feedback" is no longer a separate mode — it is simply the agent calling `gleipnir.ask_operator`. "Policy-gated approval" is unchanged.
- ADR-009 is superseded in mechanism: the feedback channel resolution (policy-first, system fallback) now applies to the `notify` package configuration rather than to MCP tool selection.

---

## ADR-032: Admin-managed OpenAI-compatible LLM provider instances

**Status:** Proposed (will be marked Accepted when the implementation of spec
`docs/superpowers/specs/2026-04-06-openai-compatible-llm-client-design.md` lands).

**Context.** Gleipnir's existing LLM provider model (ADR-026) supports two
providers — Anthropic and Google — each backed by a vendor SDK and configured
via a single fixed `<provider>_api_key` row in `system_settings`. The provider
list is a static `knownProviders` slice baked in at startup. This does not
extend to adding OpenAI as a third first-class provider (issue #533), letting
operators point Gleipnir at OpenAI-compatible backends (Ollama, vLLM,
OpenRouter, LM Studio, Together, Groq, Azure-via-compat), or allowing
administrators to add or change LLM endpoints at runtime without redeploying.

**Decision.** Introduce a second provider mechanism that coexists with the
existing SDK-backed mechanism:

- **SDK providers (`anthropic`, `google`)** remain exactly as today. One row
  per provider in `system_settings`. Static `knownProviders` slice. Vendor
  SDKs. They are inherently special — vendor-specific features (prompt
  caching, signed thinking blocks, citations, structured outputs) justify
  per-provider client code.
- **OpenAI-compatible provider instances** are admin-managed, persisted in a
  new `openai_compat_providers` table, and registered into the existing
  `ProviderRegistry` at startup and on every admin mutation. Each row is an
  *instance* of one shared client implementation: a single hand-rolled
  `*openai.Client` constructed with the row's `base_url` and decrypted
  `api_key`. The same client serves OpenAI itself
  (`base_url = https://api.openai.com/v1`) and any compatible third-party
  backend.

**Why hand-rolled, not the official OpenAI Go SDK.** OpenAI Chat Completions
is small, stable, and re-implemented by dozens of third-party backends. A
hand-rolled client (~500 lines) permits permissive deserialization that
tolerates compat-backend quirks (omitted fields, slightly different streaming
chunks, missing `/models`). A strict typed SDK would reject responses a
permissive client accepts. Maintaining one client for both OpenAI proper and
compat backends avoids the drift and bug surface of two parallel
implementations of the same protocol. The SDK's value is concentrated in
non-Chat-Completions surfaces (Realtime, Assistants, Responses) that
Gleipnir does not need.

**Why Chat Completions only, not the Responses API.** The Responses API is
OpenAI-only (compat backends do not implement it). Surfacing reasoning
content from o-series models requires it; we accept that reasoning content
is hidden and only reasoning token counts are recorded, via
`TokenUsage.ThinkingTokens`. Standard chat models have no hidden reasoning,
so nothing is lost there.

**Why two mechanisms instead of unifying everything in one table.**
Migrating Anthropic and Google into the new table was rejected because they
are legitimately special: vendor SDKs with features that don't fit a uniform
shape. The two-mechanism approach is honest about the underlying difference.

**Why the reserved-name rule.** The names `anthropic` and `google` are
reserved at the API layer. Without this, an admin could create an
`openai_compat_providers` row named `anthropic` and silently shadow the
SDK-backed Anthropic provider in the registry.

**Why API keys are encrypted at rest.** Reuses the existing
`internal/admin/crypto.go` and `GLEIPNIR_SECRET_KEY` infrastructure already
used for Anthropic and Google keys. No new key-management story.

**Why deletion is destructive without policy checks.** A policy referencing
an unknown provider already fails at run-start with a clear error. A
"references" check can be added later without changing this ADR. In-flight
runs that already hold a client reference complete their current API call
and only fail when their next run starts and tries to look up the provider
in the registry.

**Why connection-test-on-save (with a 404 escape hatch).** Surfacing bad
config to the admin at save time — rather than to a policy author hours
later in a failed run — is the better operator experience. The 404 escape
hatch exists because some compat backends do not implement `/v1/models`;
they should still be usable, with the trade-off that model-name autocomplete
is unavailable for those instances.

**Consequences.**

- New table `openai_compat_providers`. Migration is additive.
- New admin endpoints under `/api/v1/admin/openai-providers`, admin-role gated.
- New section on the existing admin LLM Providers page. Anthropic and Google
  sections unchanged.
- New Go package `internal/llm/openai`, mirroring `internal/llm/anthropic`
  and `internal/llm/google`.
- Policy YAML unchanged. Policies continue to say `provider: <name>`.
- Two parallel provider mechanisms exist after this change. Future LLM
  providers that also speak OpenAI Chat Completions require zero new code
  (just an admin-created instance). Future LLM providers that need a vendor
  SDK require a new package alongside `anthropic` and `google` and an entry
  in `knownProviders`.

**Supersedes / amends.** Builds on ADR-026 (Model-Agnostic Design); does not
supersede it. Adds a second registration mechanism alongside the existing
static one. ADR-001 (hard capability enforcement) is unchanged — the new
client never sees policy details; it only receives filtered tool lists.

**Superseded in part by ADR-033.** The hand-rolled Chat Completions client
described here was renamed to `internal/llm/openaicompat` and is now used
exclusively for admin-managed third-party backends. OpenAI's own API now uses
the official `openai-go` SDK targeting the Responses API (`internal/llm/openai`).
The reserved-name list was extended with `"openai"` to prevent compat rows from
shadowing the premium provider.

---

## ADR-033: Premium OpenAI client split from OpenAI-compatible client

**Status:** Accepted
**Date:** 2026-04

**Context.** ADR-032 introduced a single hand-rolled Chat Completions client
(`internal/llm/openai`) serving both OpenAI's own API and any OpenAI-compatible
third-party backend. This provided compat tolerance but left OpenAI as a
second-class provider — unlike Anthropic and Google, it had no built-in startup
registration and no access to OpenAI-specific features (Responses API, reasoning
tokens, structured outputs).

**Decision.** Split the single role into two:

- **`internal/llm/openai`** — a premium OpenAI client using the official
  `github.com/openai/openai-go` SDK targeting the **Responses API**. Registered
  at startup from the `openai` entry in `knownProviders`, exactly like Anthropic
  and Google. The API key is stored in `system_settings` via the existing admin
  key-management flow.
- **`internal/llm/openaicompat`** — the renamed hand-rolled Chat Completions
  client, used exclusively by `LoadAndRegister` for admin-managed compat rows
  (Ollama, vLLM, OpenRouter, etc.). No behavioral change to the compat path.

**Why now.** The Responses API provides first-class reasoning tokens
(`OutputTokensDetails.ReasoningTokens`), a typed output surface, and reasoning
summary blocks — capabilities unavailable via Chat Completions. The symmetry
with Anthropic and Google (three premium SDK clients + one generic compat
loader) makes the provider model immediately readable.

**Why the Responses API, not Chat Completions.** The Responses API is OpenAI's
current-generation interface. It exposes reasoning items natively, handles
multi-turn state cleanly via the input list, and surfaces per-turn token usage
including reasoning tokens. Chat Completions does not expose reasoning content.
The compat client's Chat Completions path remains available for backends that
need it (compat backends do not implement the Responses API).

**Why reserve `"openai"` at the admin layer.** Without this, an admin could
create a compat row named `"openai"` and, depending on load order, shadow the
premium provider in the registry. The premium providers are registered first;
the compat loader runs after. The reserved-name check makes the invariant
explicit and prevents the ambiguity entirely.

**Consequences.**

- `internal/llm/openai` — new package, `openai-go` SDK, Responses API.
- `internal/llm/openaicompat` — renamed from `internal/llm/openai`. Hand-rolled
  Chat Completions. Compat behavior unchanged.
- `main.go` — `"openai"` added to `knownProviders`; `case "openai"` added to
  the provider-construction switch.
- `internal/admin/openai_compat_handler.go` — `"openai"` added to `reservedNames`.
- `OPENAI_API_KEY` is the env variable whose presence at startup is warned about
  (matches Anthropic/Google pattern — keys are managed through the admin UI).

**Builds on.** ADR-026 (model-agnostic design), ADR-032 (OpenAI-compat loader).

---

## Open Decisions

### Filter expression syntax
**Decision:** JSONPath. Battle-tested, libraries available in Go, readable in a UI field.
**Status:** Decided in principle, library selection pending.

### Reasoning storage format
**Leaning:** SQLite rows per step: run_id, step_number, type (thought/tool_call/tool_result/approval_request/complete), content JSON, timestamp, token_cost.

### Auth model
**Leaning:** Single-user v1, optional basic auth via env config.

### Poll trigger MCP client
**Open:** The poll trigger needs to call an MCP tool to check for new work. This happens outside
an agent run — the trigger engine itself needs a lightweight MCP client. Decide when building
the poll trigger.

### Stdio MCP transport
**Future:** HTTP first for v1. Stdio support for running MCP servers as local processes to be added later.
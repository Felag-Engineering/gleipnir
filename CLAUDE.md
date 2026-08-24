## Project overview

Gleipnir is a homelab-scale autonomous agent orchestrator. It runs AI agents with hard capability enforcement (no prompt-based restrictions), a full audit trail, and human-in-the-loop controls.

## Commands

**Backend:**
```bash
make tools               # install the pinned sqlc + buf the drift lanes need
                         # (fresh worktrees/containers have neither on PATH)
sqlc generate            # regenerate internal/db/ from internal/db/queries/*.sql
make ci-local            # PR CI gate locally, narrowed to your diff; safe on a
                         # dirty tree — this is the dev-loop's pre-PR merge gate
make ci-local-full       # same gate, no narrowing (every lane, every package)
```

**`ci-local` is scoped to your diff.** `scripts/ci-local-scope.sh` picks the lanes and
packages the change can actually reach: a docs-only change races no Go packages, a
frontend-only change runs no Go tests, and a leaf-package change races that package plus
its reverse-dependency closure. `go build ./...` and every lint/drift lane always run
whole-tree. Anything the scoper can't reason about — a change to the `Makefile`,
`scripts/`, or the root `go.mod` — widens back to the full gate automatically.

**A scoped pass is an inner-loop signal, not full coverage.** CI runs the entire matrix
on the pushed branch, and that is what a merge decision rests on. The gate prints
`SCOPED` or `FULL` on both ends of the run so a green result is never read as more
coverage than it had. `scripts/ci-local-scope-self-test.sh` (gated in CI, and part of
`ci-local`'s own lint lane) pins the properties that make narrowing safe — chiefly that
the changed package and its transitive dependents are never dropped.

**Do not pass `-j` to `ci-local`.** The lanes are memory-bound (Go linker, staticcheck,
`tsc`), so the target sizes its own parallelism from available RAM and holds a
machine-wide lock while it runs — two concurrent gates OOM-killed a 4 GiB host mid-run.
A second worktree's gate queues instead of racing. `scripts/ci-local.sh` has the
measurements and the `CI_LOCAL_JOBS` / `CI_LOCAL_NO_LOCK` / `CI_LOCAL_FULL` escape hatches.

**Frontend:** see `frontend/CLAUDE.md` for dev/build/test commands.

**Environment variables** (with defaults):

| Variable | Default | Description |
|----------|---------|-------------|
| `GLEIPNIR_DB_PATH` | `/data/gleipnir.db` | SQLite file path |
| `GLEIPNIR_LISTEN_ADDR` | `:8080` | HTTP listen address |
| `GLEIPNIR_LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `GLEIPNIR_MCP_TIMEOUT` | `30s` | Timeout for MCP server calls |
| `GLEIPNIR_HTTP_READ_TIMEOUT` | `15s` | HTTP server read timeout |
| `GLEIPNIR_HTTP_WRITE_TIMEOUT` | `15s` | HTTP server write timeout |
| `GLEIPNIR_HTTP_IDLE_TIMEOUT` | `60s` | HTTP server idle timeout |
| `GLEIPNIR_APPROVAL_SCAN_INTERVAL` | `30s` | How often to check for timed-out approvals |
| `GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT` | `30m` | Default timeout for feedback requests |
| `GLEIPNIR_FEEDBACK_SCAN_INTERVAL` | `30s` | How often to check for timed-out feedback |
| `GLEIPNIR_PLUGIN_REQUEST_SCAN_INTERVAL` | `30s` | How often to check for timed-out plugin channel requests (reclaims stranded `plugin_pending_requests` rows whose in-memory waiter died on host restart) |
| `GLEIPNIR_DRAIN_TIMEOUT` | `5m` | Graceful-shutdown drain timeout for in-flight runs and background loops (poller, cron runner, scheduler, timeout scanners, run manager — all joined in one timeout-raced goroutine, #487). |
| `GLEIPNIR_PID_FILE` | `/var/run/gleipnir.pid` | Path the server writes its PID to on startup |
| `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS` | `false` | When `true`, the loader accepts unsigned plugin bundles (per-instance health `unsigned_permissive` + yellow chip; high-severity audit event per load; `/api/v1/health` reports `signature_verification: disabled`). **Signed plugins are still fully verified.** Global scope, read once at startup. ADR-045 §6. |
| `GLEIPNIR_CONTAINER_RUNTIME_MODE` | `auto` | Which container-runtime socket the substrate talks to: `auto`, `rootless-podman`, `docker`, or `manual` (ADR-056 §7). `auto` probes standard locations, preferring rootless Podman — whose socket carries your user's authority rather than root's. `manual` is never auto-selected: silently disabling writes an operator did not ask to disable would be worse than failing to start. Under `docker` a startup WARN states plainly that the socket is approximately root on the host. |
| `GLEIPNIR_CONTAINER_SOCKET` | *(unset)* | Explicit socket path replacing the standard-location probe. Only meaningful together with an explicit (non-`auto`) mode. |
| `GLEIPNIR_PLUGINS_DIR` | `/plugins` | Directory watched by the fsnotify watcher for plugin tarballs (`.tar.gz`/`.tgz`). |
| `GLEIPNIR_OAUTH_REFRESH_INTERVAL` | `5m` | How often the OAuth refresh scanner runs to proactively refresh plugin OAuth2 tokens approaching expiry (#224). |
| `GLEIPNIR_OAUTH_REFRESH_LEAD` | `15m` | Lead-time window before token expiry within which the scanner triggers a refresh attempt. |
| `GLEIPNIR_PLUGIN_DEDUP_SWEEP_INTERVAL` | `10m` | Sweep cadence for evicting `plugin_event_dedup` rows older than the fixed 1-hour dedup window (#562; only the cadence is configurable). |
| `GLEIPNIR_LLM_RETRY_MAX_ATTEMPTS` | `4` | Total attempts (including the first) for transient LLM API failures (connection errors, 408/429, transient 5xx). `1` disables retry. Drives the Anthropic/OpenAI SDKs' own retry count and the manual loop for Google + openaicompat. |
| `GLEIPNIR_LLM_RETRY_INITIAL_BACKOFF` | `1s` | Base wait for the manual retry loop (exponential, full-jitter). A provider `Retry-After` hint is honored verbatim instead. |
| `GLEIPNIR_LLM_RETRY_MAX_BACKOFF` | `30s` | Ceiling for any single retry wait (including a provider `Retry-After`); the run context bounds total wait. |
| `GLEIPNIR_ELICITATION_MAX_REQUEST_STATE_BYTES` | `16384` | Largest opaque MRTR `requestState` blob accepted from one `input_required` result (ADR-055, spec §6.2 cap 2). Oversize is a structural error: the call fails and nothing is persisted. |
| `GLEIPNIR_ELICITATION_MAX_REQUESTS` | `8` | Most elicitations one `input_required` result may bundle. |
| `GLEIPNIR_ELICITATION_MAX_REQUESTS_BYTES` | `65536` | Largest serialized `inputRequests` array accepted from one result. |
| `GLEIPNIR_ELICITATION_RATE_PER_SEC` | `1` | Sustained per-MCP-server token-bucket rate for `input_required` results (spec §6.2 cap 3). A token bucket, not a debounce — the spec rejects heuristics here. Over-limit results are refused before decoding. |
| `GLEIPNIR_ELICITATION_BURST` | `5` | Burst ceiling for the same bucket. |
| `GLEIPNIR_PLUGIN_SUBNET_POOL` | `10.83.0.0/16` | Base CIDR the reconciler carves per-instance plugin networks from, one `/24` each (ADR-056, spec §7). The default holds 256 instances. Must be IPv4 and no longer than a `/24`. Operators running other workloads on the same daemon should also widen the daemon's own `default-address-pools` so the two allocators do not collide — see `docs/developer/container-networking.md`. |
| `GLEIPNIR_ENCRYPTION_KEY` | *(required)* | 64-char hex key (32-byte AES-256) for encrypting provider API keys and webhook secrets; generate with `openssl rand -hex 32` |

**Provider API keys:** All LLM provider API keys (Anthropic, Google, OpenAI, and any OpenAI-compatible backends) are configured through the admin UI at `/admin/models` and stored encrypted in the database. Env vars like `ANTHROPIC_API_KEY` / `GOOGLE_API_KEY` / `OPENAI_API_KEY` are intentionally ignored — a startup warning is logged if they are set.

**Encryption key rotation:** stop the server first, then run `gleipnirctl rotate-key` (supports `--dry-run`) — full procedure in `cmd/gleipnirctl/README.md`.

## Architecture

The Go server handles policy management, agent orchestration, and the reasoning trace. It talks to external MCP servers over HTTP to discover and invoke tools. The React frontend is embedded in the Go binary via `go:embed` (built in Docker from `frontend/dist`) and served directly by the Go HTTP server. SQLite (WAL mode) is the only datastore, embedded in the Go container.

See `frontend/CLAUDE.md` for detailed frontend architecture (routes, design system, component structure, hooks, testing).

## Core domain concepts

**Agent** — a run scoped to a specific policy. Tools not granted to a run are never registered with the agent; they do not exist from the agent's perspective.

**Policy** — YAML config defining trigger, agent task prompt, capability grants, and limits. Six trigger types: `webhook`, `manual`, `scheduled`, `poll`, `cron`, `subscribed`.

**Trigger types:**
- `webhook` — HTTP POST to `/api/v1/webhooks/{policyID}` fires a run
- `manual` — operator triggers a run from the UI or API
- `scheduled` — one-shot runs at specific ISO-8601 timestamps (defined via `fire_at` list in policy YAML; auto-pauses when exhausted)
- `poll` — recurring polling with MCP tool invocations and JSONPath condition checks
- `cron` — recurring runs on a 5-field POSIX cron expression (`cron_expr`); runs indefinitely until paused
- `subscribed` — plugin-sourced events: policy binds to a manifest-declared `(source, event_kind)` pair with typed binding filters, not JSONPath (ADR-048; internal-only name — the UI never shows the word "subscribed").

**Capabilities** — two categories, tracked in Gleipnir's own DB (not in MCP servers):
- `tool` — MCP tools the agent can call, optionally approval-gated, optionally parameter-scoped (ADR-017)
- `feedback` — human-in-the-loop channel; agent sends a message and waits for operator response (ADR-031)

**Run states:**
```
pending → running → complete | failed
                  → waiting_for_approval → running (approved) | failed (rejected/timeout)
                  → waiting_for_feedback → running (response received) | failed (timeout)
running | waiting_for_approval | waiting_for_feedback → interrupted (on restart)
```

**Step types** (reasoning trace entries): `capability_snapshot`, `thought`, `thinking`, `tool_call`, `tool_result`, `approval_request`, `feedback_request`, `feedback_response`, `error`, `complete`.

**Approval modes:**
1. Agent-initiated: agent calls gleipnir.ask_operator voluntarily
2. Policy-gated: tools marked `approval: required` are intercepted by the runtime before execution — hard guarantee, not prompt-based

**User roles:** `admin`, `operator`, `approver`, `auditor`. Roles control API endpoint access via middleware.

## Key packages

See `docs/developer/architecture.md` for the full package dependency graph (Mermaid diagram).

```
schemas/
  policy.yaml         — schema that defines how policies will be stored
  sql_schemas.sql     — human-readable reference snapshot of the full datastore. NOT consumed by sqlc or the migration runner (sqlc reads internal/db/migrations/0001_initial.sql + the two .sql migrations listed in sqlc.yaml); keep it in sync by hand with the post-migration schema. The migration NNNN_ filename prefix is a hint only — Version() (via the All() slice in registry.go) is authoritative for apply order; prefixes may carry a suffix (e.g. 0025b_) to avoid collisions (#492).

cmd/
  gleipnirctl/        — local admin CLI; direct DB-level maintenance operations (rotate-key, reset-password, create-user). Run via `docker compose run --rm api gleipnirctl <command>`.

internal/
  admin/              — admin HTTP handlers (provider/model API keys, OpenAI-compat backends, system settings) + AES-256-GCM helpers for at-rest secret encryption
  arcade/             — Arcade.dev REST client + toolkit pre-authorization helpers (ADR-040)
  db/                 — sqlc-generated data access layer; queries live in internal/db/queries/
  execution/          — agent runtime subsystem
    agent/            — BoundAgent runner, LLM API loop, audit writer
    run/              — run lifecycle: RunManager (goroutine tracking), RunLauncher (concurrency + launch), AgentFactory, RunsHandler. `RunLauncher.LaunchWithConcurrency` is the single seam every trigger path goes through: skip/queue are non-error outcomes in a typed `LaunchResult`; wrapped DB errors classify via `IsConcurrencyCheckError`/`IsEnqueueError`; all six trigger handlers are thin adapters over it (#503).
    runstate/         — canonical run status transition table and TransitionRunFailed helper
  http/               — HTTP layer subsystem
    api/              — router builder (RouterConfig + BuildRouter), chi route handlers, validation middleware, response helper re-exports
    auth/             — authentication, sessions, user management, role middleware
    httputil/         — shared HTTP response helpers (JSON envelope encoding)
    sse/              — Server-Sent Events broadcaster
  infra/              — cross-cutting leaf packages; no internal imports
    config/           — environment variable loading
    event/            — Publisher interface for internal pub/sub
    headervalidate/   — HTTP header-name validation (RFC 7230 + reserved-name blocklist — see the package for the authoritative list; reserved `Mcp-*` names track the 2026-07-28 protocol client work, #734/#737/#741 — Mcp-Method/Mcp-Name are now client-set on modern-pinned traffic). Extracted from `internal/mcp` so `internal/plugin/oauth` can validate `header_set` entries without an import cycle (#226). This blocklist is not the whole story for server-declared `x-mcp-header` tool-parameter headers: `internal/mcp` layers its own denylist + `[A-Za-z0-9-]` name allowlist on top (#747), because there the name is chosen by the remote server and the value by the model. Do not widen `ReservedHeaderNames` to cover that case — it is shared with the ADR-039 admin write path, where e.g. `Authorization` is a legitimate operator choice.
    logctx/           — context-based structured log correlation (run_id + policy_id)
    metrics/          — custom Prometheus registry, histogram bucket presets (BucketsFast/BucketsSlow), shared label constants, Handler()/Registry() accessors (ADR-037)
    version/          — single-source-of-truth release version constant; bump in the commit immediately preceding a release tag
  llm/                — LLM provider abstraction (ADR-026): per-provider wire-level translation (ProviderWire) behind one shared ProviderAdapter; FakeWire drives agent/trigger tests through the real adapter; per-wire SchemaFeatures() declarations gate the shared lossy-translation pass (ADR-059). Seam details: internal/llm/CLAUDE.md.
    anthropic/        — Anthropic API client
    contract/         — cross-wire contract test suite (external test package, outside internal/llm to avoid the provider-import cycle) (#506)
    factory/          — NewClientForProvider: maps a provider name string to a concrete LLMClient; lives in its own sub-package to avoid the cycle caused by provider packages importing internal/llm
    google/           — Google AI client
    openai/           — OpenAI API client
    openaicompat/     — OpenAI-compatible provider loader (bootstraps third-party OpenAI-compatible backends)
  mcp/                — MCP HTTP client, tool registry, capability tags. Also hosts the client side of Gleipnir's own MCP extensions: `io.gleipnir/channel` (`channel.go`, #800 — `channel/notify` + `channel/request`, the latter returning a Tasks task). Extensions are negotiated in the `server/discover` capabilities map, NOT by attempting a method: on the 2026-07-28 transport there is no `initialize`, and a host that probed by calling would be delivering messages to find out whether it could. Contract: `docs/developer/extension-io-gleipnir-channel.md`. **Managed plugin endpoints** (`managed.go` + `gate.go`, #819) make a healthy generation an ordinary `mcp_servers` row — one client stack, one namespace, one canonical-schema path; a parallel table would reintroduce the second path ADR-053 exists to delete. The trust tier is DERIVED from `plugin_instance_id`, never a second column that could disagree with it; `io.gleipnir/*` is gated on it (external declarations are dropped, since believing one would let a pasted-in URL be asked to settle a human approval), and the zero value is external. One row per INSTANCE, not per generation: a rotation repoints `url`, which keeps the tool-namespace prefix stable across an upgrade AND is itself the routing flip, because `url` is part of the cache's invalidation key while an already-resolved `*Client` keeps its old base URL and drains against its own generation. Per-server concurrency + queue depth (`gate.go`, defaults matching `plugin/dispatch`) bound two different things — work in flight is the server's problem, callers waiting is Gleipnir's — and `ErrQueueFull` returns without waiting. Managed rows are not operator-editable (409, not 403 — an admin has every permission and still cannot). Full design: `docs/developer/managed-mcp-endpoints.md`.
  model/              — domain types (Policy, Run, RunStep, ApprovalRequest, enums, ...)
  plugin/             — host-side plugin substrate: loader (fsnotify watcher, tarball extractor, minisign verifier, DB installer), subprocess + generation lifecycle, identity-token auth, hostsvc host-RPC server, tool/channel/trigger dispatch, binding evaluation, event dedup, and OAuth orchestration. Sub-package map with invariants: internal/plugin/CLAUDE.md.
  policy/             — YAML parser, validator, system prompt renderer
  schemanorm/         — stdlib-only leaf package: byte-level normalization of JSON Schema tool-input documents (object keys recursively sorted, semantics byte-identical — no structural transforms; duplicate keys, invalid UTF-8, and numeric re-rendering are rejected, not accommodated). ADR-059 / spec §10 step 1; wired at MCP discovery — `internal/mcp` canonicalizes each tool schema and persists it to `mcp_tools.canonical_schema`, which drift detection compares (#738). Full design history and invariants: `internal/schemanorm/CLAUDE.md`.
  settings/           — read-side accessor for system_settings (default model, public URL); injected into runtime so non-admin packages don't depend on the admin HTTP handler
  testutil/           — shared test helpers
  timeout/            — generic scan-and-resolve loop for expiring requests, plus the approval and feedback scanner constructors (NewApprovalScanner / NewFeedbackScanner) wired in main.go; agent-side approval/feedback request handling lives in execution/agent/
  toolregistry/       — neutral leaf package: in-memory uniqueness arbiter for the shared `<source>.<tool>` namespace. Imported by `internal/mcp` (MCP-side reservations) and `internal/plugin/tools` (plugin-side reservations); neither side imports the other (issue #194).
  trigger/            — trigger dispatch only: webhook, manual, scheduled, poll, and cron handlers (imports execution/run/ for launching)
```

**ADRs:** track decisions in docs/developer/ADR_Tracker.md. When a decision settles, add a one-line constraint under `## Settled architectural decisions` below AND the full statement to `docs/developer/settled-decisions.md`. Do not reference ADRs in source code; do reference them in commit and PR messages.

## Code style

**Readable and understandable first.** This codebase should be easy to read and reason about for anyone picking it up. Prefer code that is immediately clear over code that is compact or "elegant". When in doubt, optimize for the next reader.

**Explicit over clever.** If there's a straightforward way and a clever way, write the straightforward way.

**Strict error handling.** Never swallow errors. Wrap with context: `fmt.Errorf("context: %w", err)`.

**Tests alongside new code.** Table-driven tests for anything with branching logic, error paths, or concurrency behavior. Don't test trivial getters. Do test:
- State machine transitions
- Error paths (missing tool, token budget exceeded, MCP server unreachable)
- Concurrent audit writes
- Context cancellation propagation

**Testing time-dependent code.** Anything depending on `time.Now()` must route through an injectable package-level clock (`var timeNow = func() time.Time { return time.Now() }`); tests swap it via `t.Cleanup`, never wall-clock timing. Tests that mutate the shared clock must not use `t.Parallel()`; advancing a fake clock also refills `rate.Limiter` tokens — drain the burst first. Canonical example: `internal/plugin/hostsvc/event_ratelimit_test.go`; full rules + war stories: `docs/developer/testing-patterns.md`.

**Drain launched runs before cleanup.** Any test that can launch a run must register `t.Cleanup(mgr.Wait)` **after** the `testutil.NewTestStore` call (cleanup is LIFO — the RunManager drains before the store closes). A test that starts a **trigger source** (Scheduler/Poller/CronRunner) must additionally cancel + join that source *before* the manager drain — register its cleanup after `t.Cleanup(mgr.Wait)`; a still-running source reaching `Launch` races `mgr.Wait` (undefined per `sync.WaitGroup`, #787). Use `testutil.NewMockLLMClient()` for runs that can reach the LLM; `NewNoopLLMClient` (panics on any call) only for paths that provably never launch. Full pattern: `docs/developer/testing-patterns.md`.

**Signal-don't-poll.** Never wait for an async side effect on a tight wall-clock poll — synchronize on an event the system already publishes (e.g. `capturePublisher.waitForEvent`), with the deadline as a generous CI bound. Unavoidable wall-clock waits use deadlines at least 5× the expected duration. Full pattern: `docs/developer/testing-patterns.md`.

**Comments explain why, not what.** Non-obvious decisions get a brief inline comment. Architectural reasoning belongs in ADRs — reference them by number in code comments when relevant.

**Package boundaries are intentional.** `internal/mcp` must have no import dependencies on `internal/execution/agent`. The poll trigger engine reuses the MCP client directly — a tight coupling here requires refactoring later.

## Key API surface

Routes are registered in `internal/http/api/router.go` via `BuildRouter`, which constructs the complete route tree from a `RouterConfig` struct. `main.go` constructs dependencies and passes them to `BuildRouter`.

**Response envelope:** `{ data: T }` for success, `{ error: string, detail?: string }` for failure.

**Key endpoint groups:**
- `/api/v1/auth/*` — login, logout, setup, sessions, password change
- `/api/v1/policies/*` — CRUD for policies
- `/api/v1/runs/*` — list/get runs, steps, cancel, approval, feedback; `GET|POST /:runID/tool-input` reads and answers a tool-initiated HITL pause (ADR-055). Read access matches the rest of run detail; the *write* role gate is per-row, not per-route — `permission` requests need `approver`, `information` requests need `operator` (`model.ElicitationKind.RequiredRole`), so the route middleware only excludes auditors and the handler decides. The GET may carry `prior_attempt` (spec §6.5): a server whose MRTR state expired and then re-asked a *different* question re-prompts the operator with the previous question and answer attached. An *identical* re-ask never reaches an operator at all — the stored answer is replayed silently against the fresh `requestState`, once per question. `GET /:runID/decisions` returns the run's tool-initiated HITL **decision records** (ADR-055 §6.6, #803) — a separate endpoint from `/steps` rather than extra entries in it, because `/steps` is the trace the model is replayed and oversight evidence is not part of it (ADR-046). Read-only and readable by auditors; a combined timeline is assembled client-side.
- `/api/v1/mcp/servers/*` — MCP server registry, tool discovery; `PUT /:id` updates name/url only; `PUT /:id/headers/:name` and `DELETE /:id/headers/:name` manage individual auth headers (admin|operator only; see ADR-039); `POST /:id/arcade/authorize` and `POST /:id/arcade/authorize/wait` pre-authorize Arcade toolkits (admin|operator only; see ADR-040)
- Plugin admin surface (`/api/v1/admin/plugins*`, plugin instances, audiences, plugin credentials/OAuth) — full endpoint contracts live in `.claude/rules/plugin-admin-api.md` (loads when working under `internal/admin`, `internal/http/api`, or `internal/plugin`).
- `PUT /api/v1/admin/settings/default-model` — admin-only: set the system default LLM model `{provider, name}` (ADR-035; 400 on missing provider key, 422 on disabled model).
- `/api/v1/webhooks/{policyID}` — fires a webhook-triggered run (auth dispatcher per `trigger.auth`: hmac | bearer | none)
- `/api/v1/policies/{policyID}/trigger` — fires a manual run
- `/api/v1/policies/{id}/webhook/rotate`, `/api/v1/policies/{id}/webhook/secret` — rotate/reveal the webhook secret (admin|operator only; see ADR-034)
- `/api/v1/config` — public instance config (`public_url`, `default_model`); available to all authenticated roles
- `/api/v1/events` — SSE stream (`text/event-stream`) for real-time updates
- `/api/v1/models` — list/refresh available LLM models
- `/api/v1/stats`, `/api/v1/stats/timeseries` — dashboard statistics
- `/api/v1/attention` — items needing operator attention. Four item types: `approval`, `feedback`, `tool_input`, `failure`. A `tool_input` row carries `elicitation_kind` (the §6.1 permission/information split, which decides who may answer) and `untrusted_message: true` (the text is server-controlled). The queue offers no inline approve for it — the question, its schema, its deadline source, and any prior attempt live on the run detail card, and a one-click approval in the queue would be consent given without having read the ask (#804)
- `/api/v1/users/*` — user management (admin)
- `/api/v1/settings/preferences` — user preferences
- `/api/v1/health` — health check

## Settled architectural decisions

These are resolved constraints — do not re-litigate them. Each is compressed to the binding constraint; the full statements with mechanics are preserved in `docs/developer/settled-decisions.md`, and ADR bodies live in `docs/developer/ADR_Tracker.md`.

- **Hard capability enforcement (ADR-001):** disallowed tools are never registered with the agent. Prompt-based restrictions are not a control mechanism and must not be used as one.
- **Policy stored as a YAML blob (ADR-002):** only `name` and `trigger_type` are indexed columns; every other policy field lives in the `yaml` column. No separate data model.
- **SQLite, WAL mode, no ORM (ADR-003):** WAL enabled at the application layer; audit writes serialized through an application-layer queue; all queries go through sqlc.
- **MCP HTTP transport only (ADR-004):** capability tags (`tool`/`feedback`) are Gleipnir metadata in Gleipnir's DB — never stored in the MCP server.
- **Package boundary:** `internal/mcp` must not import `internal/execution/agent`.
- **Policy-gated approval is a hard runtime guarantee (ADR-008):** `approval: required` tools are intercepted before execution, regardless of agent reasoning.
- **Feedback channel resolution (ADR-009):** policy-level channel definition falls back to system-level config.
- **SSE for real-time UI transport (ADR-016):** mutations remain REST; no WebSockets.
- **Policy-level parameter scoping (ADR-017):** per-policy `params` blocks narrow the tool schema before the agent sees it, and bound which argument names dispatch accepts. The two halves have different reach (#769): the **allowlist is structural for every schema shape** — `mcp.ValidateCall` derives it from the `params` block itself, so a root-level `oneOf`/`anyOf`/`$ref` tool is scoped exactly like a plain object one — while **narrowing reaches only top-level `properties`**, so for those shapes the agent is shown an unnarrowed schema and may attempt a property that then fails the run. A params block **never blocks a save**; unenforceable-narrowing cases warn instead. Real branch-keyword narrowing is still deferred: describe scoping as structurally enforced, but not as fully reflected in what the agent sees.
- **Capability snapshot as first run step (ADR-018):** every run records the exact tools registered at run start.
- **Agent editor (ADR-019):** Form view is the only editing surface; YAML is the API payload and storage format (YAML tab removed in #751).
- **Policy folders are YAML-only (ADR-020):** optional `folder` string, purely cosmetic; no DB column.
- **Model-agnostic design (ADR-026):** providers behind the `internal/llm` interface; the agent runtime is provider-agnostic.
- **Tool risk classification (ADR-028):** risk levels affect approval requirements.
- **Approval state machine (ADR-029):** minimal v1.0 lifecycle with timeout enforcement.
- **Protocol-agnostic tools page (ADR-030):** the Tools page never exposes MCP-specific concepts.
- **Native feedback (ADR-031):** feedback is a first-class runtime primitive (`gleipnir.ask_operator`, `waiting_for_feedback`, timeout). UI term is "Feedback request" for both native and audience Request flows (#656); internal identifiers are intentionally NOT renamed.
- **Webhook secrets in encrypted DB column (ADR-034):** `webhook_secret_encrypted` lives outside the YAML blob (GET returns the YAML wholesale); `trigger.auth` mode stays in YAML; rotate/reveal is admin|operator only.
- **DB-backed system settings (ADR-035):** `system_settings` key/value table, admin-managed; non-sensitive values exposed to all roles via `GET /api/v1/config`.
- **Atomic run-state transitions (ADR-038):** every status change is a version-CAS transaction; callers must not assume their write won.
- **Authenticated MCP servers (ADR-039):** encrypted static auth headers, write-only over the API (GET returns key names only); per-header PUT/DELETE; reserved header names rejected; per-policy/per-user scoping deferred.
- **Arcade gateway pre-authorization (ADR-040):** toolkit pre-auth via Arcade REST; credentials reuse ADR-039 headers; per-policy user_id deferred.
- **Plugin system architecture (ADR-041):** go-plugin subprocesses over gRPC/UDS; three services (Tool/Channel/Trigger); one shared `<source>.<tool>` namespace; filesystem-dropin distribution only.
- **Plugin service versioning (ADR-042):** per-service SemVer on four axes; two-major-version deprecation window; enum additions are additive.
- **Plugin signing tooling (ADR-043):** bundled Minisign Go library (`gleipnir-plugin keygen|sign|package`); no external binary.
- **Channel routing model (ADR-044):** `Notify` (fire-and-forget fan-out) vs `Request` (exactly-one, async callback); admin-managed ordered audiences; `gleipnir.in-app` auto-appended fallback unless disabled; six credential strategies; best-effort `RequestTerminated` close-the-loop RPC (#625).
- **Plugin signing & TOFU trust (ADR-045):** TOFU pubkey pinning at first install; key rotation needs admin approval; material manifest changes block hot-reload; `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS` is the dev escape hatch.
- **Audit-table split (ADR-046):** `run_steps` = LLM-visible (replayed into context); `plugin_audit_events` = operational/security (never enters context); `WriteAuditStep` accepts only `feedback_response`.
- **Plugin observability (ADR-047):** plugin metrics force-prefixed `gleipnir_plugin_` + auto-labelled, hard 100-value cardinality cap with loud rejection; plugin logs ride the `Log` host RPC, not stdout; OTel deferred to v2.
- **Subscribed trigger type (ADR-048):** internal-only name; typed binding fields from `binding_schema`, not JSONPath; never shown to operators.
- **Redact-on-read for plugin config secrets (ADR-049):** `x-gleipnir-secret` fields read back as `"***"`; bulk PUT rejects the sentinel; per-field write-only PUT; fail-closed (500) on unparseable manifest.
- **Dispatcher separation (ADR-051):** tool-call pool, channel dispatcher, and trigger dispatcher stay separate; identity/generation gating lives in the hostsvc interceptors. Do not merge them.
- **Operator-selectable binding operators (ADR-052):** decided, implementation deferred — per-policy explicit `{op, value}` with flat-value back-compat; `mention_only` stays fixed-semantic. Do not re-litigate operator selectability.
- **MCP 2026 realignment (ADR-053…ADR-060, target `v0.2.0-alpha` — decided 2026-07-30; the §11 MCP-client half has shipped (#733–#748, #762; #769 closed the branch-keyword scoping gap — the allowlist is now enforced from the `params` block itself), and the §6 tool-initiated HITL half has now shipped too (#793–#805). The §5 events half has shipped too (milestone #16, #898–#904, #906; PRs #907–#919). The §7 container substrate is BUILT BUT NOT LIVE — milestone #15 merged (#806–#820), yet the reconciler is not started by `main.go`, so the v1.1 gRPC plugin architecture is still what actually runs — and the §5 events listen supervisor shares that same built-not-live status for the same reason. §8 host endpoint, §9 user scoping and the §12 cutover are not started):** plugins become signed containerized MCP servers (hard cutover, no dual-running); events move to the `io.gleipnir/events` extension (host-captured, never injected into model context); HITL rides MRTR + Tasks through audiences. Amendment 1 (2026-08-04, spec §0): *host-initiated ⇒ not a tool* — channel delivery is the `io.gleipnir/channel` extension, events carry CloudEvents envelopes, and server→host callbacks are a host-side MCP endpoint (host-plane only, never grantable) — no gRPC/protobuf anywhere in the target plugin contract. Full body: `docs/developer/mcp-realignment-spec.md`. Until it lands, the v1.1 plugin architecture is the live system — do not mix the two.
- **Tool-initiated HITL decision records are not step types (ADR-055 §6.6 refinement, #803):** the spec names `tool_permission_request` / `tool_input_request` "alongside" `approval_request` / `feedback_request`, but that is an analogy to their shape, not to where they live. They are `plugin_audit_events.event_type` values, never `model.StepType` values — making them step types would make them eligible for context replay by construction, and would hand a server a route into the model's context through an operator's own answer. Operator-facing docs: `docs/user/human-in-the-loop.md`.
- **Weak-assurance channels are skipped, not refused (ADR-055 §4.1 refinement, #802):** a channel that may not settle a request kind causes the audience to fall through to the next entry, and every skip is recorded with a typed reason on the decision record. `ErrNoEligibleEntry` is reachable only when an operator disabled the in-app fallback, since the synthetic entry is `authenticated` and settles both kinds — that makes exhausting the audience a configuration fault worth failing loudly rather than a routine outcome.
- **ADR-054 refinements (milestone #16):** mandatory SSE heartbeat at declared `heartbeatMs` with a 3×-silence dead-stream rule; the one coined CloudEvents extension attribute is `gleipnirseq`; each event rides an `events/event` notification, clean close is a JSON-RPC response `{reason, cursor}`; an unsatisfiable/malformed cursor is refused with JSON-RPC error `-32001` before any stream opens, and the client resets and reconnects from empty.
- **CSS Modules, no inline styles:** all frontend styling via CSS Modules consuming CSS custom properties; no inline `style={}`.
- **4px spacing scale:** margins, padding, and gaps snap to multiples of 4px.

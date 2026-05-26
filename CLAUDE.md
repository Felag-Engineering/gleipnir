## Project overview

Gleipnir is a homelab-scale autonomous agent orchestrator. It runs AI agents with hard capability enforcement (no prompt-based restrictions), a full audit trail, and human-in-the-loop controls.

## Commands

**Backend:**
```bash
go build ./...           # build
go test ./...            # run all tests
go test ./internal/...   # run only internal package tests
sqlc generate            # regenerate internal/db/ from internal/db/queries/*.sql
docker compose up        # run full stack (Go binary with embedded frontend)
```

**Frontend:** (run from `frontend/`)
```bash
npm run dev              # Vite dev server (proxies /api → localhost:8080)
npm run build            # TypeScript check + production build
npx vitest run           # run Vitest unit tests
npm run storybook        # Storybook on port 6006
```

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
| `GLEIPNIR_DEFAULT_PROVIDER` | `anthropic` | Default LLM provider |
| `GLEIPNIR_PLUGINS_ENABLED` | `true` | Enable the host-side plugin loader (on by default; set to `false` to opt out for one more release before flag removal; see docs/developer/plugin-system-spec.md §15.2). |
| `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS` | `false` | When `true`, the loader accepts plugins lacking a Minisign `.minisig`/`signing.pub` pair (instance health = `unsigned_permissive`). Every load emits a high-severity audit event; admin UI shows a non-dismissible red banner; `/api/v1/health` reports `signature_verification: disabled`. **Signed plugins are still fully verified even in permissive mode.** Scope is global, not per-plugin. Read once at startup. See ADR-045 §6. |
| `GLEIPNIR_PLUGINS_DIR` | `/plugins` | Directory watched by the fsnotify watcher for plugin tarballs (`.tar.gz`/`.tgz`). Only consulted when `GLEIPNIR_PLUGINS_ENABLED=true`. |
| `GLEIPNIR_OAUTH_REFRESH_INTERVAL` | `5m` | How often the OAuth refresh scanner runs to proactively refresh plugin OAuth2 tokens approaching expiry (#224). Only consulted when `GLEIPNIR_PLUGINS_ENABLED=true`. |
| `GLEIPNIR_OAUTH_REFRESH_LEAD` | `15m` | Lead-time window before token expiry within which the scanner triggers a refresh attempt. Only consulted when `GLEIPNIR_PLUGINS_ENABLED=true`. |
| `GLEIPNIR_ENCRYPTION_KEY` | *(required)* | 64-char hex key (32-byte AES-256) for encrypting provider API keys and webhook secrets; generate with `openssl rand -hex 32` |

**Provider API keys:** All LLM provider API keys (Anthropic, Google, OpenAI, and any OpenAI-compatible backends) are configured through the admin UI at `/admin/models` and stored encrypted in the database. Env vars like `ANTHROPIC_API_KEY` / `GOOGLE_API_KEY` / `OPENAI_API_KEY` are intentionally ignored — a startup warning is logged if they are set.

**Encryption key rotation:** To rotate `GLEIPNIR_ENCRYPTION_KEY`, stop the server first and run:
```bash
docker compose stop api
printf '%s\n%s\n' "$OLD_KEY" "$NEW_KEY" | docker compose run --rm api gleipnirctl rotate-key --old - --new -
```
This re-encrypts all at-rest secrets (provider API keys, OpenAI-compat keys, webhook secrets) in a single transaction. Use `--dry-run` to validate the old key covers every ciphertext without committing changes. See `cmd/gleipnirctl/README.md` for full usage.

## Stack

- **Backend:** Go, [chi](https://github.com/go-chi/chi) router, [sqlc](https://sqlc.dev/) for type-safe queries, multi-provider LLM support (Anthropic + Google)
- **Frontend:** React + TypeScript (Vite), CSS Modules, CodeMirror 6 (YAML editor), TanStack Query, SSE for real-time updates, Storybook for component dev, embedded in the Go binary via `go:embed` and served directly
- **Storage:** SQLite with WAL mode
- **Deployment:** Docker Compose
- **Tool protocol:** MCP over HTTP transport

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
- `subscribed` — plugin-sourced events. Policy binds to a `(source, event_kind)` pair declared by an installed plugin's manifest; binding filters use typed form fields (not JSONPath) derived from the manifest's `event_kinds[].binding_schema`. Internal-only type — UI never renders the word "subscribed" (per ADR-048). Runtime dispatch lands in #214.

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

See `docs/architecture.md` for the full package dependency graph (Mermaid diagram).

```
schemas/
  policy.yaml         — schema that defines how policies will be stored
  sql_schemas.sql     — schema that explains the different tables in our datastore

cmd/
  gleipnirctl/        — local admin CLI; direct DB-level maintenance operations (rotate-key, reset-password). Run via `docker compose run --rm api gleipnirctl <command>`.

internal/
  approval/           — approval-specific timeout wiring (thin wrapper over timeout/)
  arcade/             — Arcade.dev REST client + toolkit pre-authorization helpers (ADR-040)
  db/                 — sqlc-generated data access layer; queries live in internal/db/queries/
  execution/          — agent runtime subsystem
    agent/            — BoundAgent runner, LLM API loop, audit writer
    run/              — run lifecycle: RunManager (goroutine tracking), RunLauncher (concurrency + launch), AgentFactory, RunsHandler (HTTP endpoints for run inspection/control), sentinel concurrency errors
    runstate/         — canonical run status transition table and TransitionRunFailed helper
  feedback/           — feedback-specific timeout wiring (thin wrapper over timeout/, ADR-031)
  http/               — HTTP layer subsystem
    api/              — router builder (RouterConfig + BuildRouter), chi route handlers, validation middleware, response helper re-exports
    auth/             — authentication, sessions, user management, role middleware
    httputil/         — shared HTTP response helpers (JSON envelope encoding)
    sse/              — Server-Sent Events broadcaster
  infra/              — cross-cutting leaf packages; no internal imports
    config/           — environment variable loading
    event/            — Publisher interface for internal pub/sub
    headervalidate/   — HTTP header-name validation (RFC 7230 + reserved-name blocklist `Mcp-Session-Id`, `Content-Type`, `Accept`, `Content-Length`, `Host`). Stdlib + `golang.org/x/net/http/httpguts` only. Extracted out of `internal/mcp` so `internal/plugin/oauth` can validate `header_set` credential entries without an `oauth → mcp → admin → oauth` cycle (#226).
    logctx/           — context-based structured log correlation (run_id + policy_id)
    metrics/          — custom Prometheus registry, histogram bucket presets (BucketsFast/BucketsSlow), shared label constants, Handler()/Registry() accessors (ADR-037)
    version/          — single-source-of-truth release version constant; bump in the commit immediately preceding a release tag
  llm/                — LLM provider abstraction (ADR-026)
    anthropic/        — Anthropic API client
    factory/          — NewClientForProvider: maps a provider name string to a concrete LLMClient; lives in its own sub-package to avoid the cycle caused by provider packages importing internal/llm
    google/           — Google AI client
    openai/           — OpenAI API client
    openaicompat/     — OpenAI-compatible provider loader (bootstraps third-party OpenAI-compatible backends)
  mcp/                — MCP HTTP client, tool registry, capability tags
  model/              — domain types (Policy, Run, RunStep, ApprovalRequest, enums, ...)
  plugin/             — host-side plugin loader; gated by GLEIPNIR_PLUGINS_ENABLED. Implements fsnotify watcher, tarball extractor, minisign verifier, DB installer, and subprocess lifecycle (process/). Per-instance UDS is provided transitively by go-plugin's broker. Identity tokens are issued at spawn via `internal/plugin/identity` and delivered to the subprocess via the `GLEIPNIR_INSTANCE_TOKEN` env var (constant `serve.InstanceTokenEnvVar`); the SDK's outgoing-metadata interceptor reads the env and attaches the token to every host RPC, where `hostsvc.UnaryInstanceTokenInterceptor` validates it. Phase 4 substrate (subprocess spawning #291, per-instance identity token #292, generation refcounts #294, hostsvc activation #295) is complete; remaining Phase 5 work tracked under #159 covers ChannelService and admin UI surfaces, not the host-RPC plumbing.
    state/            — per-instance health-state machine; mirrors execution/runstate (transition table + version-CAS, ADR-038). Severity ranking encodes §8.1 "plugin can only mark itself worse" merge rule.
    manifest/         — host-side manifest diff (material vs cosmetic) consumed by the loader on hot-reload (spec §5.4). Schema-shape comparison strips `description`/`default` keys at every depth; depends only on plugin-sdk/manifest + stdlib.
    tools/            — plugin-side tool registrar; reserves `<instance>.<tool>` names with `internal/toolregistry`, writes `plugin_tool_namespace_conflict` audit events on collision, drives the instance to `unhealthy` (issue #194), and tracks per-instance generation consulted by `internal/execution/agent` to fail tool calls bound to a replaced generation with a structural error (issue #195, ADR-018).
    identity/         — leaf package; in-memory `(token → instanceID)` registry that authenticates plugin Host RPCs (spec §8.4, issue #202). `Issue(instanceID)` mints a 256-bit base64url token and atomically auto-revokes any prior token for the same instance — a killed-generation token cannot impersonate the new generation. `Lookup(token)` returns the stable instance ID; `Revoke`/`RevokeInstance` cover subprocess-exit and uninstall paths. No internal imports.
    audience/         — audience composition. `Resolve(rows)` materialises an audience's effective entry list and auto-appends a synthetic `gleipnir.in-app` entry (`Auto:true`, lowest priority) unless `disable_in_app_fallback=true`. `ValidateAudienceCoverage` rejects an opt-out save when no other Request-capable entry exists. Synthetic entry is materialised at read-time only — never persisted in `audience_entries`. `Dispatcher.Request` returns `(requestID, RoutingOutcome, err)`: `RouteToPlugin` for persisted entries (writes `plugin_pending_requests`), `RouteToInApp` for the synthetic entry (no DB row; agent-side caller routes via the existing `inAppChannel`).
    binding/          — leaf package (stdlib + yaml.v3 only); runtime evaluator for subscribed-trigger bindings (spec §7.3, issue #217). `Compile(binding map[string]any, schema *yaml.Node)` resolves the operator per field from the manifest's `binding_schema` (Option C — `type:string,format:regex` → regex, `format:contains` → contains, plain `type:string` → equals, field `mention_only` + `type:boolean` → mention-only) and pre-compiles regexes; `(cb).Evaluate(payload map[string]any)` is pure bool/error with implicit AND across keys. Missing payload field or type mismatch is silent (no-fire, not an error) — plugins are external; a runtime error should not crash the dispatcher. `format:glob` is reserved for future use (`ErrUnsupportedOperator`). Nested binding paths and OR semantics are v2.
    configvalidate/   — generic plugin-config validator built on `santhosh-tekuri/jsonschema/v6`. Compiles a manifest's reflected `config_schema` (YAML) once per content-hashed snapshot and surfaces field-level errors as `[]FieldError{Field, Message}` for the upcoming audience save endpoint (#290) and reused by trigger-binding (#216) and instance-config (#241) validation. `ForChannelAudience` rejects manifests without a `ChannelService` (`ErrNotChannelPlugin`); `ForTriggerBinding` linear-scans `EventKinds` (no [0]).
    dedup/            — leaf package (stdlib-only); placeholder for the event-dedup window (#215). `Store` interface with `Seen(key) (bool, error)` and `Noop` default that always returns `(false, nil)`. The trigger dispatcher consults it before fan-out, so #215 can drop in a real implementation without touching call sites.
    trigger/          — host-side subscribed-trigger plumbing (issue #214). `Supervisor` opens the long-lived `TriggerService.Start` server stream per instance (single drain goroutine — per-stream ordering preserved), restarts on EOF/error with bounded exponential backoff + ±25% jitter, and reports health via the Manager's HealthSetter. `Dispatcher.Handle` consumes `EmittedEvent` from the hostsvc trigger sink: dedup check (`dedup.Store`), scan `GetSubscribedActivePolicies`, compile binding from the plugin's manifest `event_kinds[].binding_schema`, evaluate against payload, launch matching policies via `RunLauncher.Launch` (mirrors cron/poll, not the HTTP-coupled `internal/trigger/launch.go`). `SinkAdapter` lives here to break the hostsvc↔trigger cycle. Late-bound into `hostsvc.Server` via `SetTriggerSink` after `RunLauncher` is constructed in main.go.
    dispatch/         — gRPC tool-call dispatcher over UDS to plugin instances; per-call deadline matches `GLEIPNIR_MCP_TIMEOUT`, per-instance concurrency semaphore + bounded queue gate (spec §13.2/§13.6), and run-cancellation fan-out via `Cancel(call_id)` with 5s deadline + force-disconnect fallback (spec §13.8, issue #198). Wired into the agent fork point through `agent.PluginToolDispatcher`; receives cancel hooks from `internal/execution/run.RunManager`.
    process/          — per-instance subprocess lifecycle. Thin wrapper around plugin-sdk/hostwire (HashiCorp go-plugin) that adds identity-token issuance, per-instance env injection (GLEIPNIR_INSTANCE_ID / GLEIPNIR_PLUGIN_ID / GLEIPNIR_INSTANCE_TOKEN), per-instance log labels (plugin= / instance=), and graceful Stop / crash health callbacks. Imports plugin-sdk/hostwire, plugin-sdk/serve (for `InstanceTokenEnvVar`), internal/plugin/identity, and internal/infra/logctx. Manager (manager.go) imports internal/plugin/state to build the HealthSetter callback; the core process.go / waitForExit logic drives health state only via an injected callback, never importing the state package directly. `Manager.ReloadInstance` (issue #294) drives a generation rotation: `BeginDrain` → `stopWithoutUnregister` → `Start`; `ManagerConfig.GenerationController` is optional and nil-safe so non-plugin code paths keep working.
    generation/       — leaf package (stdlib-only); per-(instance_id, generation) refcount + drain controller for plugin Host RPCs (spec §8.4, issue #294). `Controller.RegisterInstance` is idempotent and never resets the counter. `Acquire(ctx, instanceID)` increments the refcount under the current generation and returns a wrapped cancellable context; new traffic is held in `Unavailable` (caller-deadline-bounded) while a drain is in progress. `BeginDrain(ctx, instanceID, grace)` bumps the generation, waits for old-gen refcount to reach zero within `grace`, then force-cancels stragglers via the wrapped contexts (mirror of dispatcher's §13.8 5s force-disconnect, but via `context.CancelFunc` rather than `conn.Close` because host RPCs are ordinary unary handlers that respect ctx). `UnregisterInstance` wakes any blocked `Acquire` callers. Concurrent `BeginDrain` callers serialise: the second returns `"drain already in progress"`. Generation type is `uint64`; this counter is independent of `internal/plugin/tools.Registrar.Generation` (`int64`, different epoch).
    hostsvc/          — host-side gRPC server for plugin→host RPCs (spec §8.1/§8.2/§8.5, issues #199 + #200 + #201). `UnaryCallIDInterceptor` reads the `gleipnir-call-id` metadata into context; `RejectIfDetached` is preserved for future RPCs that require call-scope binding; `WriteAuditStep` instead authenticates via request-ownership per spec §8.5 (the request_id's `plugin_pending_requests.plugin_instance_id`, or the native `feedback_request`'s policy-grant by stable instance name), so substrate-initiated callbacks (e.g. Slack Socket Mode click events) do not need to synthesize a call_id. `Server` implements the eight always-on Tier-1 RPCs (`GetInstanceConfig`, `GetCredentials`, `GetRunContext`, `WriteAuditStep`, `EmitMetric`, `EmitEvent`, `Log`, `SetHealthState`); resolves `(run_id, policy_id)` from the propagated `call_id` via `dispatch.Pool.LookupCall`; reuses `internal/infra/{logctx,metrics,event}` rather than parallel systems. `EmitMetric` force-prefixes `gleipnir_plugin_`, auto-injects `plugin`/`instance` labels, enforces a 100-distinct-values per-(metric,label-key) cardinality cap, and rejects inconsistent label-key sets (no panics from `GaugeVec.With`). `WriteAuditStep` accepts only `feedback_response` step type — anything else writes an `unauthorized_step_type` audit event. Tier-2 RPCs `RunHistoryRead` and `UserDirectoryRead` are gated by the manifest's `tier2_capabilities` list; an undeclared capability returns PermissionDenied + an `unauthorized_tier2_call` audit event. `RunHistoryRead` is scoped to policies that reference the calling instance via tool grants (`<instance>.<tool>` prefix in `capabilities.tools`); `UserDirectoryRead` returns only `(user_id, username, role)` tuples — no credentials or session data. `UnaryInstanceTokenInterceptor` reads `gleipnir-instance-token` metadata, looks the token up in `internal/plugin/identity.Registry`, and attaches the resolved instance ID to the request context for `contextBinder` to read; missing/unknown tokens are rejected with `codes.Unauthenticated` before the handler runs (spec §8.4, issue #202). `WriteAuditStep` additionally enforces that a `feedback_response`'s `request_id` belongs to the calling instance: it walks `feedback_request → run → policy.yaml` and rejects with `unauthorized_request_id` (severity high) when no `<instance_name>.<tool>` capability prefix is granted — the check is by stable instance name so it survives generation rotation. `UnaryGenerationRefcountInterceptor` (issue #294) chains after the token interceptor and routes each RPC through `internal/plugin/generation.Controller.Acquire`/`Release` so a generation rotation can drain in-flight calls before the new generation accepts traffic. `hostsvc.Server` is constructed once at boot in `main.go`, attached to every subprocess via `Manager.HostServerFor`, and gated behind the `[token, generation, call-id]` interceptor chain plumbed through `hostwire.Options.ServerInterceptors`.
    e2e/              — host-side substrate composition tests (issue #237). Exercises the real audience → binding → dedup → trigger → dispatch path against an in-process stub plugin so the host+plugin composition is gated per-PR without spawning a real plugin binary. External test package (`package e2e_test`); registers a gRPC `TriggerServiceServer` directly on `127.0.0.1:0` mirroring `internal/plugin/trigger/supervisor_test.go`. No Slack-specific code — Slack-shaped scenarios are covered by the nightly Playwright spec at `tests/playwright/slack-kitchen-sink.spec.ts`.
    oauth/            — host-side OAuth2 orchestration for plugin instances (spec §9.1/§9.2, issue #224). `Manager.BeginAuthcode` builds an `oauth2.Config`, encodes an HMAC-signed state envelope (HKDF-derived subkey off `GLEIPNIR_ENCRYPTION_KEY`, info `"gleipnir-oauth-state-v1"`), records a one-time nonce, and returns the provider authorize URL. `Manager.HandleCallback` verifies + consumes the state envelope, exchanges the code via `cfg.Exchange`, persists the token, and emits `plugin_oauth_issued`. `Manager.BeginClientcred` runs the client-credentials grant synchronously. `DBStore` encrypts `StoredCredentials` (strategy + endpoints + token) into `plugin_instances.credentials_encrypted`; `EncryptFunc`/`DecryptFunc` are injected at construction time to avoid an `internal/admin` import cycle (the admin OAuth handler imports this package). `SaveToken` runs a load→modify→save CAS retry (3 attempts, no sleep) gated by a per-instance mutex (`locks.go`), and skips the write when a concurrent refresher already stored a fresher token. `NewTokenSource` wraps `oauth2.Config.TokenSource` / `clientcredentials.Config.TokenSource` in a persisting decorator that calls `SaveToken` + `EmitRefreshed` on rotation and `MarkRefreshFailed` on inner error — `MarkRefreshFailed` drives the instance to `PluginHealthStateUnhealthy` for #228's "Re-authorize" UI. `RefreshScanner` mirrors `internal/timeout/scanner.go`: every `GLEIPNIR_OAUTH_REFRESH_INTERVAL` it queries `ListPluginInstancesWithExpiringCredentials` for rows expiring within `GLEIPNIR_OAUTH_REFRESH_LEAD`, builds the TokenSource, and calls `.Token()` (the wrapper handles the actual refresh + persist). The scanner skips authcode-strategy rows when `public_url` is unset. The callback path `/api/v1/admin/plugins/oauth/callback` is registered OUTSIDE the `requireAuth` middleware (browser arrives from the OAuth provider with no Gleipnir session) — the HMAC state envelope is the sole auth primitive, mirrors ADR-034.
  policy/             — YAML parser, validator, system prompt renderer
  settings/           — read-side accessor for system_settings (default model, public URL); injected into runtime so non-admin packages don't depend on the admin HTTP handler
  testutil/           — shared test helpers
  timeout/            — generic scan-and-resolve loop for expiring requests (used by approval/ and feedback/)
  toolregistry/       — neutral leaf package: in-memory uniqueness arbiter for the shared `<source>.<tool>` namespace. Imported by `internal/mcp` (MCP-side reservations) and `internal/plugin/tools` (plugin-side reservations); neither side imports the other (issue #194).
  trigger/            — trigger dispatch only: webhook, manual, scheduled, poll, and cron handlers (imports execution/run/ for launching)
```

**ADRs:** Architectural decisions are referenced in docs/developer/ADR_Tracker.md, decisions should be tracked there and this document should be updated anytime architectural decisions are made. Do not reference in source code but do reference in commit messages and PR messages.

## Code style

**Readable and understandable first.** This codebase should be easy to read and reason about for anyone picking it up. Prefer code that is immediately clear over code that is compact or "elegant". When in doubt, optimize for the next reader.

**Explicit over clever.** If there's a straightforward way and a clever way, write the straightforward way.

**Strict error handling.** Never swallow errors. Wrap with context: `fmt.Errorf("context: %w", err)`.

**Tests alongside new code.** Table-driven tests for anything with branching logic, error paths, or concurrency behavior. Don't test trivial getters. Do test:
- State machine transitions
- Error paths (missing tool, token budget exceeded, MCP server unreachable)
- Concurrent audit writes
- Context cancellation propagation

**Testing time-dependent code.** Anything whose behavior depends on `time.Now()` — rate limiters, timeouts, scheduled jobs, audit-coalescing windows — must route through an injectable package-level clock variable (convention: `var timeNow = func() time.Time { return time.Now() }`), and tests must swap it via `t.Cleanup` rather than relying on wall-clock timing. The integration test for issue #222 originally asserted `>= 50` drops to absorb token-bucket refill noise during the loop's wall-clock duration; CI flaked when the loop ran 20ms (2 tokens refilled, 48 drops). The fix: route `rate.Limiter.Allow()` through `AllowN(timeNow(), 1)` — `x/time/rate` exposes the `*N(t time.Time, ...)` variants exactly for this — then freeze `timeNow` in the test for exact-equality assertions (`drops == total - burst`). The same pattern applies to `time.AfterFunc`, `time.NewTimer`, and any custom scheduling. Concrete rules: (1) production code never calls `time.Now()` directly inside hot paths that tests need to assert against — wrap in `timeNow()` instead; (2) tests that mutate a shared package-level clock variable must not run with `t.Parallel()`; (3) when `t.Parallel()` is needed, use external test packages with a `SetTimeNowForTest(fn) (restore func())` export hook; (4) be aware that advancing a fake clock also refills `rate.Limiter` tokens — drain the burst at each new timestamp before probing drop behavior. See `internal/plugin/hostsvc/event_ratelimit.go` and `event_ratelimit_test.go` for the canonical pattern.

**Signal-don't-poll.** When a test waits for an asynchronous side effect (DB row inserted, status transition committed, audit step flushed), do **not** poll on a tight wall-clock deadline — synchronize on an event the system already publishes. Tests in `internal/execution/agent/` use `capturePublisher.waitForEvent(t, eventType, timeout)` to block on a specific event-bus message; the deadline becomes a generous CI-tolerance bound (seconds, not milliseconds) rather than a real assertion. If you find yourself writing `time.Sleep` inside a `for time.Now().Before(deadline)` loop, ask whether there is a publisher, channel, or callback that fires the moment the work is done — that signal is the correct synchronization primitive. Tight polling budgets ("100ms should be enough") are how CI flakes are born. The few wall-clock waits that genuinely cannot be turned into signals (e.g. waiting for a `time.NewTimer` to fire when the production code can't yet take an injectable clock) must use deadlines at least 5× the expected duration so CI scheduling jitter cannot exceed them.

**Comments explain why, not what.** Non-obvious decisions get a brief inline comment. Architectural reasoning belongs in ADRs — reference them by number in code comments when relevant.

**Package boundaries are intentional.** `internal/mcp` must have no import dependencies on `internal/execution/agent`. The poll trigger engine reuses the MCP client directly — a tight coupling here requires refactoring later.

## Key API surface

Routes are registered in `internal/http/api/router.go` via `BuildRouter`, which constructs the complete route tree from a `RouterConfig` struct. `main.go` constructs dependencies and passes them to `BuildRouter`.

**Response envelope:** `{ data: T }` for success, `{ error: string, detail?: string }` for failure.

**Key endpoint groups:**
- `/api/v1/auth/*` — login, logout, setup, sessions, password change
- `/api/v1/policies/*` — CRUD for policies
- `/api/v1/runs/*` — list/get runs, steps, cancel, approval, feedback
- `/api/v1/mcp/servers/*` — MCP server registry, tool discovery; `PUT /:id` updates name/url only; `PUT /:id/headers/:name` and `DELETE /:id/headers/:name` manage individual auth headers (admin|operator only; see ADR-039); `POST /:id/arcade/authorize` and `POST /:id/arcade/authorize/wait` pre-authorize Arcade toolkits (admin|operator only; see ADR-040)
- `GET /api/v1/admin/plugins` — admin-only: list all installed plugins with name, version, status, instance count, and parsed manifest metadata (services, tier-2 capabilities, auth strategy, pubkey fingerprint, SBOM presence). Includes plugins in `pending_review` status that have no instances yet (#242).
- `GET /api/v1/admin/plugins/{id}` — admin-only: plugin detail with full parsed manifest for the review consent surface. Returns services, tier-2 capabilities, auth strategy, pubkey fingerprint, SBOM flag, author, and license. Used by the `PluginReviewPage` (#242).
- `POST /api/v1/admin/plugins/{id}/approve` — admin-only: transitions a `pending_review` plugin to `active` status. 409 when plugin is not in `pending_review`. Emits `plugin_review_approved` audit event (#242).
- `POST /api/v1/admin/plugins/{id}/reject` — admin-only: deletes a `pending_review` plugin (DB row + bundle directory). 409 when plugin is not in `pending_review`. Emits `plugin_review_rejected` audit event before deletion. Bundle can be re-dropped into `/plugins` to re-install (#242).
- `POST /api/v1/admin/plugins` — admin-only: install a plugin from an uploaded `application/octet-stream` tarball (max 100 MiB; route registered outside the `/api/v1/admin` group to override the group's 1 MiB body cap). Writes a temp file in `os.TempDir()` (not the plugins directory, to avoid racing the fsnotify watcher), invokes the same `internal/plugin/loader.Installer.Install` flow used by the watcher, and returns `{id, name, version, status}`. Status reflects the post-install row state — e.g. `pending_key_approval` / `pending_review` for pubkey-mismatch and material-change rejected installs (audit-only, row unchanged). 422 when signature is invalid (audit-only success path); 503 when the loader is disabled (`GLEIPNIR_PLUGINS_ENABLED=false`); 400 on malformed tarball; 409 on CAS conflict (#379).
- `POST /api/v1/admin/plugins/{id}/instances` — admin-only: create a plugin instance with `{instance_name}`. Seeds defaults `config_json="{}"`, `subscription_scope_json="{}"`, `handshake_versions="{}"`, `health_state="unhealthy"`, `health_detail="config_missing"`. Pre-checks `GetPluginInstanceByName` for a clean 409; falls back to substring-matching the SQLite UNIQUE-constraint error for the race path. The handler does NOT seed OAuth credentials — that's a follow-up (#379, defers to a later issue).
- `DELETE /api/v1/admin/plugins/{id}` — admin-only: uninstall a plugin. Refuses with 409 when any instances still exist (admin must delete all instances first — no cascade, per #243). Best-effort `StopByPluginID` before the transaction (10s deadline, logged on failure). Inside one tx, clears `plugin_pending_requests` + `plugin_oauth_nonces` per instance (defensive), then `DELETE FROM plugins` (cascades to `plugin_instances`; `plugin_audit_events.plugin_instance_id` is SET NULL so history survives). After commit, removes the bundle dir under `GLEIPNIR_PLUGINS_DIR` with a `filepath.Rel` containment check (fail-closed). Emits `plugin_uninstalled` audit event. 503 when `h.store == nil`; 404 when plugin missing (#390, updated in #243).
- `DELETE /api/v1/admin/plugins/{id}/instances/{iid}` — admin-only: delete a single plugin instance (hard remove). Gated on zero in-flight tool calls, zero policy subscribed triggers, and zero audience entries referencing the instance — 409 with `blockers` array listing offending dependents with names when any gate fails. Admin must decouple dependencies first (no cascade). Same best-effort `Stop` + in-tx cleanup shape as uninstall, scoped to one instance. Emits `plugin_instance_deleted` (#390, updated in #243).
- `POST /api/v1/admin/plugins/{id}/instances/{iid}/deactivate` — admin-only: soft deactivate a plugin instance. Stops subprocess and trigger stream, transitions health to `inactive`. Gated on zero in-flight tool calls (409 if calls in progress). Reversible via activate. Emits `plugin_instance_deactivated` audit event (#243).
- `POST /api/v1/admin/plugins/{id}/instances/{iid}/activate` — admin-only: reactivate a deactivated instance. Transitions health from `inactive` to `unhealthy`, spawns subprocess and restarts trigger stream. 409 if instance is not in `inactive` state. Emits `plugin_instance_activated` audit event (#243).
- `/api/v1/admin/plugins/{id}/instances/{iid}` — per-instance plugin health (state, detail, version) for the admin plugins UI; admin-only
- `PUT /api/v1/admin/plugins/{id}/instances/{iid}/subscription-scope` — admin-only: writes the coarse instance-level watch scope (validated against the manifest's `subscription_schema`), CAS-guarded via `expected_version`, and synchronously calls `triggerSupervisor.Restart(instanceID)` so the plugin reopens its substrate connections under the new scope (#223). 400 when the manifest declares no `subscription_schema`; 422 on schema-validation failure; 409 on stale version.
- `/api/v1/admin/plugins/{id}/accept-new-key` — admin-only TOFU rotation: rotates `plugins.trusted_pubkey` to an operator-supplied candidate pubkey and unblocks instances stuck in `pending_key_approval` (see ADR-045 §5.3)
- `/api/v1/admin/audiences` — audience CRUD for plugin channel routing (CRUD + `GET /{id}/references` for save-guard policy/in-flight-run lookups). admin|operator for mutations; auditor read-only. Save path chains config-schema validation (#206), manifest capability validation (#207), and the auto-append + disable-fallback rule (#208). PUT uses ADR-038 version-CAS (409 on stale). DELETE rejects with 409 (`detail` lists policy names) when policies still reference the audience by name.
- `/api/v1/admin/plugins/{id}/accept-manifest` — admin-only: commits a hot-reload's blocked candidate manifest (sourced from the latest `plugin_manifest_material_change` audit event), updates `plugins.manifest_snapshot`, and either unblocks instances to `healthy` or transitions them to `pending_config_migration` when the new schema introduces newly-required fields (spec §5.4)
- `/api/v1/admin/plugin-instances` — read-only listing of installed plugin instances enriched with channel capabilities and declared `event_kinds` (each kind now includes `binding_schema` and `examples`). Consumed by the audience editor (#290) and the policy editor's trigger picker (#219). admin|operator|auditor.
- `POST /api/v1/admin/plugin-instances/{iid}/event-kinds/{kind}/test-binding` — stateless binding evaluator preview (#220): compiles a candidate binding against the instance's manifest schema and evaluates it against caller-supplied example payloads, returning per-payload match/error. admin|operator|auditor.
- `POST /api/v1/admin/plugins/{id}/instances/{iid}/oauth/begin` — admin-only: starts the host-side OAuth2 dance for an instance. For `oauth2_authcode` returns `{authorize_url}` for the browser to follow; for `oauth2_clientcred` performs the token exchange synchronously and returns `{status:"ok"}`. 400 for non-OAuth strategies (#224).
- `/api/v1/admin/plugins/{id}/instances/{iid}/credentials` — admin-only write-only credential surface for the four non-OAuth strategies (#226). `GET` returns a redacted view (strategy + key names only, never values). `DELETE` clears the encrypted blob but preserves `Strategy`. `PUT .../credentials/static-api-key` (body `{header_name, scheme?, api_key}`), `PUT .../credentials/headers/{name}` (body `{value}`), `DELETE .../credentials/headers/{name}` (idempotent), `PUT .../credentials/basic-auth` (body `{username, password}`). The endpoint rejects with 400 when the instance's manifest `Strategy` does not match (e.g. PUT static-api-key on a `header_set` instance). `header_set` PUT runs through `internal/infra/headervalidate.ValidateName` so the ADR-039 reserved-header blocklist applies. Mirrors the write-only + encrypted-column pattern of ADR-034 / ADR-039.
- `PUT /api/v1/admin/plugins/{id}/instances/{iid}/credentials/oauth-token` — admin-only advanced escape hatch: directly seeds an OAuth access/refresh token for instances whose strategy is `oauth2_authcode` or `oauth2_clientcred`. Body `{access_token, refresh_token?, expires_at?}` (RFC3339 for `expires_at`). 400 when strategy is not an OAuth2 variant. The canonical happy path remains the authcode UI flow via `/oauth/begin`.
- `PUT /api/v1/admin/plugins/{id}/instances/{iid}/config` — admin-only: replaces the instance-level config blob (CAS-guarded via ADR-038). Body `{config: object, expected_version: int}`. Validates against the manifest's `config_schema` when declared (422 on failure); a nil schema accepts any object. 400 when any secret field (marked `x-gleipnir-secret: true` in the manifest) carries the value `"***"` (the redaction sentinel — prevents round-trip clobber per ADR-049). 409 on stale version. Does NOT restart the trigger stream (use `subscription-scope` for scope changes). Response redacts secret fields to `"***"`.
- `PUT /api/v1/admin/plugins/{id}/instances/{iid}/config/{property}` — admin-only per-field secret update; mirrors `PUT /mcp/servers/:id/headers/:name` (ADR-039). Body `{value: any, expected_version: int}`. 404 when the property is not declared in the manifest's `config_schema`. 400 when `value` is `"***"` (the redaction sentinel). 409 on stale version. Full merged config is validated against the schema before writing. Response redacts all secret fields to `"***"` (see ADR-049).
- `PUT /api/v1/admin/settings/default-model` — admin-only: sets the system default LLM model (ADR-035). Body `{provider: string, name: string}`. 400 when the provider has no API key configured (known providers only — OpenAI-compat providers skip this check); 422 when the model is not enabled. Writes `provider:model` to `system_settings.default_model` in colon-separated form compatible with `settings.GetSystemDefault`.
- `GET /api/v1/admin/plugins/oauth/callback` — OAuth provider callback. Singleton path; the instance ID and ReturnURL travel inside an HMAC-signed `state` envelope (spec §9.2). Intentionally unauthenticated at the session layer — the browser arrives from the provider — mirrors the webhook auth pattern (ADR-034).
- `/api/v1/webhooks/{policyID}` — fires a webhook-triggered run (auth dispatcher per `trigger.auth`: hmac | bearer | none)
- `/api/v1/policies/{policyID}/trigger` — fires a manual run
- `/api/v1/policies/{id}/webhook/rotate`, `/api/v1/policies/{id}/webhook/secret` — rotate/reveal the webhook secret (admin|operator only; see ADR-034)
- `/api/v1/config` — public instance config (`public_url`, `default_model`); available to all authenticated roles
- `/api/v1/events` — SSE stream (`text/event-stream`) for real-time updates
- `/api/v1/models` — list/refresh available LLM models
- `/api/v1/stats`, `/api/v1/stats/timeseries` — dashboard statistics
- `/api/v1/attention` — items needing operator attention
- `/api/v1/users/*` — user management (admin)
- `/api/v1/settings/preferences` — user preferences
- `/api/v1/health` — health check

## Settled architectural decisions

These are resolved constraints — do not re-litigate them.

- **Hard capability enforcement (ADR-001):** disallowed tools are never registered with the agent. Prompt-based restrictions are not a control mechanism and must not be used as one.
- **Policy stored as a YAML blob (ADR-002):** `name` and `trigger_type` are indexed columns for routing and list views; all other policy fields live in the `yaml` column. No separate data model for policy fields.
- **SQLite, WAL mode, no ORM (ADR-003):** WAL is enabled at the application layer on startup. Audit writes are serialized through an application-layer queue to avoid contention. All queries go through sqlc — raw `.sql` files only.
- **MCP HTTP transport only (ADR-004):** capability tags (`tool`/`feedback`) are Gleipnir's metadata, stored in Gleipnir's DB — not in the MCP server.
- **Package boundary:** `internal/mcp` must not import `internal/execution/agent`.
- **Policy-gated approval is a hard runtime guarantee (ADR-008):** tools marked `approval: required` are intercepted by the runtime before execution, regardless of agent reasoning.
- **Feedback channel resolution (ADR-009):** policy-level channel definition falls back to system-level config if absent.
- **SSE for real-time UI transport (ADR-016):** Server-Sent Events push run status changes, new steps, and approval events. Mutations remain REST. No WebSockets.
- **Policy-level parameter scoping (ADR-017):** tool parameters can be restricted per-policy via `params` blocks. Schema is narrowed before agent sees it — structural enforcement, not prompt-based.
- **Capability snapshot as first run step (ADR-018):** every run records the exact tools registered at run start.
- **Agent editor (ADR-019):** Form view is the only editing surface. YAML is the API payload and storage format; operators do not author it directly in the UI. YAML tab was removed in #751.
- **Policy folders are YAML-only (ADR-020):** `folder` is an optional string in the policy YAML for UI grouping. No DB column — purely cosmetic.
- **Model-agnostic design (ADR-026):** multi-provider support via `internal/llm` interface. Currently Anthropic + Google. Providers implement a common interface; agent runtime is provider-agnostic.
- **Tool risk classification (ADR-028):** tools categorized by risk level, affecting approval requirements.
- **Approval state machine (ADR-029):** minimal v1.0 approval lifecycle with timeout enforcement.
- **Protocol-agnostic tools page (ADR-030):** UI abstracts over tool transport. The Tools page does not expose MCP-specific concepts to users.
- **Native feedback (ADR-031):** feedback is a first-class runtime primitive. Agent can request operator input via `gleipnir.ask_operator`; the runtime manages the `waiting_for_feedback` state and timeout.
- **DB-backed system settings (ADR-035):** instance-level config (e.g. `public_url`) lives in a `system_settings` key/value table, editable via the admin UI at `/admin/system`. Admin-only `GET/PUT /api/v1/admin/settings` manages the table; a separate `GET /api/v1/config` endpoint (all authenticated roles) exposes non-sensitive values like `public_url` and `default_model` to operators and auditors.
- **Webhook secrets in encrypted DB column (ADR-034):** `webhook_secret_encrypted` is a dedicated column outside the YAML blob (scoped ADR-002 deviation). The `yaml` column is returned wholesale by GET /api/v1/policies/:id; storing a secret there would expose it to all authenticated roles. The `trigger.auth` mode (`hmac | bearer | none`) lives in YAML because it is configuration, not a secret. Auditors can see auth mode but cannot call the rotate/reveal endpoints (admin|operator only).
- **Atomic run-state transitions (ADR-038):** every status change runs in a transaction with a `version`-column CAS guard. Conflicts surface as `runstate.ErrTransitionConflict`; callers must not assume their write won.
- **Authenticated MCP servers (ADR-039):** `mcp_servers` rows may carry encrypted static auth headers (`auth_headers_encrypted` TEXT column) injected on every outbound MCP request. Values are write-only over the API — `GET` returns header *keys* only. Individual headers are managed via `PUT`/`DELETE /api/v1/mcp/servers/:id/headers/:name` (admin|operator); bulk `PUT /:id` does NOT touch credentials (mirrors ADR-034 webhook-secret pattern). Reserved headers (`Mcp-Session-Id`, `Content-Type`, `Accept`, `Content-Length`, `Host`) are rejected by validation. Per-policy and per-user credential scoping are explicitly deferred. `internal/mcp` imports `internal/admin` for in-registry decryption.
- **Arcade gateway pre-authorization (ADR-040):** Arcade MCP servers are detected by URL + header heuristic (`internal/arcade.IsArcadeGateway`). Toolkits are pre-authorized one click at a time via Arcade's `/v1/auth/authorize` REST API. No DB schema changes — credentials reuse `auth_headers_encrypted` from ADR-039. The `/wait` endpoint uses a 10s Arcade wait window; the frontend loops until terminal so each request stays under `GLEIPNIR_HTTP_WRITE_TIMEOUT`. Per-policy user_id and runtime auth-required handling are explicitly deferred.
- **Plugin observability surface (ADR-047):** Plugin-emitted metrics are force-prefixed `gleipnir_plugin_` and auto-labelled with `plugin`/`instance`; per-(metric, label-key) cardinality is hard-capped at 100 distinct values with loud `ResourceExhausted` rejection (not silent drop) so misconfigured high-cardinality labels surface in dev. Plugin logs ride the `Log` host RPC over gRPC/UDS rather than stdout — gRPC carries `call_id`/`run_id`/`policy_id` correlation through `internal/infra/logctx`, while stdout has no ordering or attribution guarantees. Stderr capture remains as the fallback for detached calls and pre-handshake panics; stdout is reserved by go-plugin for the handshake magic-cookie. Bucket presets reuse `internal/infra/metrics` (`BucketsFast`/`BucketsSlow`); lifecycle events ride `internal/infra/event.Publisher`. OpenTelemetry is explicitly deferred to v2.
- **Redact-on-read for plugin instance config secret fields (ADR-049):** Plugin manifest properties annotated with `x-gleipnir-secret: true` (SDK: `manifest.SecretString` typed field) are redacted to `"***"` on every GET response for the instance config endpoint. The host detects the annotation via `internal/plugin/configvalidate.SecretPropertyNames`. The bulk PUT (`PUT .../config`) rejects `"***"` as a value for secret fields to prevent round-trip clobber. A per-field write-only PUT (`PUT .../config/{property}`) mirrors the ADR-039 `PUT /mcp/servers/:id/headers/:name` pattern. GET returns 500 when the manifest cannot be parsed (fail-closed per the ADR-001 posture). Both write handlers apply redaction in the fallback synthesized-response path as well. Storage shape is unchanged — `config_json` still holds the raw value, which is already encrypted at rest. Generalizes ADR-034 (webhook secrets) and ADR-039 (MCP auth headers) to plugin instance config.
- **CSS Modules, no inline styles:** All frontend styling goes through CSS Modules consuming CSS custom properties. No inline `style={}` attributes.
- **4px spacing scale:** All margins, padding, and gaps snap to multiples of 4px (4, 8, 12, 16, 24, 32, 48, 64).

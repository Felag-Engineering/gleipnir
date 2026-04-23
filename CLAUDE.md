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
| `GLEIPNIR_ENCRYPTION_KEY` | *(required)* | 64-char hex key (32-byte AES-256) for encrypting provider API keys and webhook secrets; generate with `openssl rand -hex 32` |

**Provider API keys:** All LLM provider API keys (Anthropic, Google, OpenAI, and any OpenAI-compatible backends) are configured through the admin UI at `/admin/models` and stored encrypted in the database. Env vars like `ANTHROPIC_API_KEY` / `GOOGLE_API_KEY` / `OPENAI_API_KEY` are intentionally ignored — a startup warning is logged if they are set.

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

**Policy** — YAML config defining trigger, agent task prompt, capability grants, and limits. Five trigger types: `webhook`, `manual`, `scheduled`, `poll`, `cron`.

**Trigger types:**
- `webhook` — HTTP POST to `/api/v1/webhooks/{policyID}` fires a run
- `manual` — operator triggers a run from the UI or API
- `scheduled` — one-shot runs at specific ISO-8601 timestamps (defined via `fire_at` list in policy YAML; auto-pauses when exhausted)
- `poll` — recurring polling with MCP tool invocations and JSONPath condition checks
- `cron` — recurring runs on a 5-field POSIX cron expression (`cron_expr`); runs indefinitely until paused

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

internal/
  api/                — router builder (RouterConfig + BuildRouter), chi route handlers, validation middleware, response helper re-exports
  agent/              — BoundAgent runner, LLM API loop, audit writer
  approval/           — approval-specific timeout wiring (thin wrapper over timeout/)
  auth/               — authentication, sessions, user management, role middleware
  config/             — environment variable loading (leaf package, no internal imports)
  db/                 — sqlc-generated data access layer; queries live in internal/db/queries/
  event/              — event system for internal pub/sub
  feedback/           — feedback-specific timeout wiring (thin wrapper over timeout/, ADR-031)
  httputil/           — shared HTTP response helpers (JSON envelope encoding)
  llm/                — LLM provider abstraction (ADR-026)
    anthropic/        — Anthropic API client
    google/           — Google AI client
  logctx/             — context-based structured log correlation (run_id + policy_id); leaf package, no internal imports
  metrics/            — custom Prometheus registry, histogram bucket presets (BucketsFast/BucketsSlow), shared label constants, Handler()/Registry() accessors; leaf package, no internal imports (ADR-037)
  mcp/                — MCP HTTP client, tool registry, capability tags
  model/              — domain types (Policy, Run, RunStep, ApprovalRequest, enums, ...)
  policy/             — YAML parser, validator, system prompt renderer
  run/                — run lifecycle: RunManager (goroutine tracking), RunLauncher (concurrency + launch), AgentFactory, RunsHandler (HTTP endpoints for run inspection/control), sentinel concurrency errors
  runstate/           — canonical run status transition table and TransitionRunFailed helper
  sse/                — Server-Sent Events broadcaster
  testutil/           — shared test helpers
  timeout/            — generic scan-and-resolve loop for expiring requests (used by approval/ and feedback/)
  trigger/            — trigger dispatch only: webhook, manual, scheduled, poll, and cron handlers (imports run/ for launching)
```

**ADRs:** Architectural decisions are referenced in docs/ADR_Tracker.md, decisions should be tracked there and this document should be updated anytime architectural decisions are made. Do not reference in source code but do reference in commit messages and PR messages.

## Code style

**Readable and understandable first.** This codebase should be easy to read and reason about for anyone picking it up. Prefer code that is immediately clear over code that is compact or "elegant". When in doubt, optimize for the next reader.

**Explicit over clever.** If there's a straightforward way and a clever way, write the straightforward way.

**Strict error handling.** Never swallow errors. Wrap with context: `fmt.Errorf("context: %w", err)`.

**Tests alongside new code.** Table-driven tests for anything with branching logic, error paths, or concurrency behavior. Don't test trivial getters. Do test:
- State machine transitions
- Error paths (missing tool, token budget exceeded, MCP server unreachable)
- Concurrent audit writes
- Context cancellation propagation

**Comments explain why, not what.** Non-obvious decisions get a brief inline comment. Architectural reasoning belongs in ADRs — reference them by number in code comments when relevant.

**Package boundaries are intentional.** `internal/mcp` must have no import dependencies on `internal/agent`. The poll trigger engine reuses the MCP client directly — a tight coupling here requires refactoring later.

## Key API surface

Routes are registered in `internal/api/router.go` via `BuildRouter`, which constructs the complete route tree from a `RouterConfig` struct. `main.go` constructs dependencies and passes them to `BuildRouter`.

**Response envelope:** `{ data: T }` for success, `{ error: string, detail?: string }` for failure.

**Key endpoint groups:**
- `/api/v1/auth/*` — login, logout, setup, sessions, password change
- `/api/v1/policies/*` — CRUD for policies
- `/api/v1/runs/*` — list/get runs, steps, cancel, approval, feedback
- `/api/v1/mcp/servers/*` — MCP server registry, tool discovery
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
- **Package boundary:** `internal/mcp` must not import `internal/agent`.
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
- **CSS Modules, no inline styles:** All frontend styling goes through CSS Modules consuming CSS custom properties. No inline `style={}` attributes.
- **4px spacing scale:** All margins, padding, and gaps snap to multiples of 4px (4, 8, 12, 16, 24, 32, 48, 64).

## Project overview

Gleipnir is a homelab-scale autonomous agent orchestrator. It runs AI agents as **Fetters** — agents with hard capability enforcement (no prompt-based restrictions), a full audit trail, and human-in-the-loop controls.

## Planned stack

- **Backend:** Go, [chi](https://github.com/go-chi/chi) router, [sqlc](https://sqlc.dev/) for type-safe queries, official [Anthropic Go SDK](https://github.com/anthropics/anthropic-sdk-go)
- **Frontend:** React, served via nginx, proxies `/api` to the Go container
- **Storage:** SQLite with WAL mode
- **Deployment:** Docker Compose
- **Tool protocol:** MCP over HTTP transport

## Architecture

The Go server handles policy management, agent orchestration, and the reasoning trace. It talks to external MCP servers over HTTP to discover and invoke tools. The React frontend is served by nginx, which proxies `/api` to the Go container. SQLite (WAL mode) is the only datastore, embedded in the Go container.

## Core domain concepts

**Fetter** — an agent run scoped to a specific policy. Tools not granted to a run are never registered with the agent; they do not exist from the agent's perspective.

**Policy** — YAML config defining trigger, agent task prompt, capability grants, and limits. Three trigger types: `webhook`, `cron`, `poll`.

**Capabilities** — three categories, tracked in Gleipnir's own DB (not in MCP servers):
- `sensor` — read-only tools, called freely
- `actuator` — world-affecting tools, can require approval before execution
- `feedback` — human-in-the-loop channel; agent sends a message and waits for operator response

**Run states:** `pending → running → complete | failed | waiting_for_approval → running (approved) | failed (rejected/timeout)`; `interrupted` on restart.

**Approval modes:**
1. Agent-initiated: agent calls feedback tool voluntarily
2. Policy-gated: actuators marked `approval: required` are intercepted by the runtime before execution — hard guarantee, not prompt-based

## Key packages (planned structure)

```
schemas/
  policy.yaml       — schema that defines how policies will be stored
  sql_schemas.sql   — schema that explains the different tables in our datastore

internal/
  db/               — sqlc-generated data access layer
  model/            — domain types (Policy, Run, RunStep, ApprovalRequest, ...)
  policy/           — YAML parser, validator, system prompt renderer
  mcp/              — MCP HTTP client, tool registry, capability tags
  agent/            — BoundAgent runner, Claude API loop, audit writer
  trigger/          — webhook handler, cron scheduler, poll engine (v0.3)
  notify/           — feedback channel, notification dispatch (v0.2+)
```

## Code style

**Readable and understandable first.** This codebase should be easy to read and reason about for anyone picking it up. Prefer code that is immediately clear over code that is compact or "elegant". When in doubt, optimize for the next reader.

**Explicit over clever.** If there's a straightforward way and a clever way, write the straightforward way.

**Strict error handling.** Never swallow errors. Wrap with context:

**Tests alongside new code.** Table-driven tests for anything with branching logic, error paths, or concurrency behavior. Don't test trivial getters. Do test:
- State machine transitions
- Error paths (missing tool, token budget exceeded, MCP server unreachable)
- Concurrent audit writes
- Context cancellation propagation

**Comments explain why, not what.** Non-obvious decisions get a brief inline comment. Architectural reasoning belongs in ADRs — reference them by number in code comments when relevant.

**Package boundaries are intentional.** `internal/mcp` must have no import dependencies on `internal/agent`. The poll trigger engine reuses the MCP client directly — a tight coupling here requires refactoring later.

## Key API surface

- `POST /api/v1/webhooks/:policy_id` — fires a webhook-triggered run; request body is the trigger payload

## Settled architectural decisions

These are resolved constraints — do not re-litigate them.

- **Hard capability enforcement:** disallowed tools are never registered with the agent. Prompt-based restrictions are not a control mechanism and must not be used as one.
- **Policy stored as a YAML blob:** `name` and `trigger_type` are indexed columns for routing and list views; all other policy fields live in the `yaml` column. No separate data model for policy fields.
- **SQLite, WAL mode, no ORM:** WAL is enabled at the application layer on startup. Audit writes are serialized through an application-layer queue to avoid contention. All queries go through sqlc — raw `.sql` files only.
- **MCP HTTP transport only:** capability tags (`sensor`/`actuator`/`feedback`) are Gleipnir's metadata, stored in Gleipnir's DB — not in the MCP server.
- **Package boundary:** `internal/mcp` must not import `internal/agent`.
- **Policy-gated approval is a hard runtime guarantee:** actuators marked `approval: required` are intercepted by the runtime before execution, regardless of agent reasoning.
- **Feedback channel resolution:** policy-level channel definition falls back to system-level config if absent.

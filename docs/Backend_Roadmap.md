# Gleipnir — Backend Engineering Roadmap

> **Purpose:** This is the single source of truth for all backend engineering work on Gleipnir.
> It is structured for a planning agent to create GitHub milestones and issues, and for engineers
> to understand scope, dependencies, risks, and acceptance criteria without consulting any other
> document except the ADR log.
>
> **ADR log:** See `README` (the Architecture Decision Log). Decided ADRs are not re-litigated here —
> they are referenced by number. Open design decisions are tracked in Milestone 0.
>
> **Project summary:** Gleipnir is a self-hosted, event-driven AI agent runtime for homelab and
> DevOps operators. Agents run as BoundAgents with hard capability enforcement at the MCP
> tool-registration layer — sensors (read-only), actuators (world-affecting), and a feedback
> channel as first-class primitives. The agent literally cannot call a tool it has not been
> granted for a given run. Stack: Go, chi, sqlc, SQLite (WAL mode), official Anthropic Go SDK,
> Docker Compose, MCP over HTTP transport.
>
> **Architectural constraints (from ADRs — do not re-litigate):**
> - Capability enforcement is runtime-only. Tools not registered do not exist to the agent. Prompt-based restrictions are not a control mechanism. (ADR-001)
> - Sensor / actuator / feedback role distinction is core to the policy schema, runtime, and UI. All three must be treated distinctly. (ADR-007)
> - Policy-as-YAML stored in SQLite. The UI reads and writes YAML directly. (ADR-002)
> - Two approval modes exist simultaneously: agent-initiated (voluntary feedback tool use) and policy-gated (hard intercept before actuator execution). (ADR-008)
> - Feedback channel resolution order: policy config → system-level fallback. (ADR-009)
> - SQLite with WAL mode. Audit writes serialized through an application-layer queue. (ADR-003)
> - MCP over HTTP transport. Users register their own MCP server containers. (ADR-004)
> - Go + chi + sqlc. No ORMs. (ADR-005)

---

## Issue Label Recommendations

| Label | Use |
|---|---|
| `backend` | All Go backend issues |
| `data-model` | Schema, migrations, sqlc |
| `policy-engine` | YAML parsing, validation, rendering |
| `mcp` | MCP client, tool registry, discovery |
| `agent-runtime` | BoundAgent runner, Claude API loop |
| `triggers` | Webhook, cron, poll trigger engines |
| `approval` | Approval gates, feedback channel |
| `observability` | Health, cron miss detection, drift |
| `security` | HMAC, auth, trust model |
| `slack` | Slack integration |
| `design` | ADR and design decisions (Milestone 0) |
| `deferred` | Long-horizon items, no active milestone |
| `test` | Test-only issues |
| `docs` | Documentation and operational guides |

---

## Dependency Graph

```
Milestone 0 (Design) → unblocks everything

Milestone 1 (v0.1 MVP):
  EPIC-001 → EPIC-002, EPIC-003
  EPIC-002 + EPIC-003 → EPIC-004
  EPIC-004 → EPIC-005
  EPIC-005, EPIC-007 (frontend), EPIC-011 run in parallel once APIs stabilize

Milestone 2 (v0.2 Human-in-the-Loop):
  EPIC-004 (M1) → EPIC-006

Milestone 3 (v0.3 Full Trigger Support):
  EPIC-003 + EPIC-005 (M1) → EPIC-008
  EPIC-006 (M2) → EPIC-008 (replace mode constraint)

Milestone 4 (v0.4 Hardening):
  All prior milestones → EPIC-009

Milestone 5 (v0.5 Slack):
  EPIC-006 (M2) → EPIC-010
```

---

## Milestone 0 — Design Complete (Phase 0)

**Goal:** Resolve all open architectural decisions before any production code is written.
Each item below is a design task that produces a decided ADR. This milestone has no code output.
It unblocks all subsequent milestones.

Issues in this milestone should be labeled `design`. Definition of done for each: a written ADR
entry appended to the README ADR log, status changed from Unresolved to Decided.

---

### [DESIGN] ADR-011: v1 Approval Path — UI-only vs Slack Callbacks

**Affects:** EPIC-006 (approval gates), EPIC-010 (Slack integration)

The full approval loop has open questions around inbound network reachability. In a homelab,
Gleipnir is typically not publicly reachable, which makes Slack interactive button callbacks
(which require an inbound webhook) non-trivial to configure.

**Options to evaluate:**

1. **UI-only for v1.** Human approves from the Gleipnir web UI. No inbound network required.
   Simplest path. Slack callbacks added in v0.5 with documented tunnel setup.
2. **Slack callbacks in v0.2.** Requires operator to configure a tunnel (ngrok, Cloudflare Tunnel).
   More powerful but adds setup friction and a new security surface (callback HMAC verification required).
3. **Slack polling fallback.** Gleipnir polls Slack for a reaction or reply instead of receiving
   a callback. No inbound required but slower, messier, and fragile.

**Recommendation to evaluate:** Option 1 (UI-only for v1). The UI approval path is self-contained.
Slack callbacks as v0.5 with documented tunnel setup and mandatory HMAC verification.

**Additional questions to resolve:**
- What context does the approval message show? (minimum: tool name, proposed input JSON, run reasoning summary)
- If Slack callbacks are implemented, where is the HMAC secret stored and how is it rotated?

---

### [DESIGN] ADR-012: Run Persistence and Recovery Behavior

**Affects:** EPIC-001 (data model), EPIC-008 (cron/poll/concurrency)

A run in `waiting_for_approval` or `running` state holds its live state in a goroutine. If
Gleipnir restarts, that goroutine is gone. The question is what the system does on next boot.

**Options to evaluate:**

1. **Fail cleanly on restart.** On startup, scan for runs in `running` or `waiting_for_approval`
   and mark them `interrupted` with a clear terminal error and the last known step. Simple.
   The operator can see what happened and re-trigger if needed.
2. **Resumable runs.** Fully serialize run state (including the Claude conversation history) to
   DB at every step. On startup, reconstruct the goroutine and continue. Complex. Requires the
   entire conversation buffer to be persisted and re-hydrated correctly.

**Recommendation to evaluate:** Option 1 for v1. Fail with `interrupted` terminal state. Log the
last known step. Resumable runs are a deferred long-horizon feature.

**Questions to resolve:**
- What exactly is stored per step to make the `interrupted` state maximally informative?
- What is the user-facing message in the UI for an interrupted run?

---

### [DESIGN] ADR-013: System Prompt Default Template

**Affects:** EPIC-002 (policy engine), EPIC-004 (BoundAgent runtime)

Agent behavior within the capability envelope — how methodically it observes before acting,
whether it uses the feedback channel proactively — depends on the system prompt. The runtime
enforces hard gates, but a poorly written prompt produces an agent that barrels toward actuators
and produces worse results even when it doesn't violate policy.

**Questions to resolve:**
- Does Gleipnir ship a default BoundAgent system prompt preamble encoding the sensor/actuator/feedback
  operating philosophy? (Strong recommendation: yes — this is a product differentiator.)
- How is the agent informed of its current capability envelope at runtime?
  Current approach: `{{bound_agent.sensors}}` and `{{bound_agent.actuators}}` as template variables
  injected into the rendered system prompt at run start. Is this sufficient, or should the prompt
  be more explicit about the feedback tool's role?
- Should the UI warn if a policy's system prompt doesn't reference the feedback channel?
- Should Gleipnir enforce a minimum prompt structure, or leave it fully open to the operator?

---

### [DESIGN] ADR-014: Poll Trigger MCP Client Architecture

**Affects:** EPIC-008 (cron, poll, concurrency)

The poll trigger calls an MCP tool on an interval outside of any agent run. This means the
trigger engine itself needs MCP client capability — separate from the agent runtime's tool
execution path.

**Options to evaluate:**

1. **Shared connection pool.** The trigger engine and the agent runtime share the same
   `internal/mcp` client and connection pool. Simpler, but tight coupling between trigger
   scheduling and agent execution.
2. **Separate lightweight client.** The trigger engine instantiates its own MCP client
   (reusing the `internal/mcp` package but with its own connection lifecycle). Cleaner
   separation. The `internal/mcp` package was explicitly designed to have no import
   dependencies on the agent runtime — this option leverages that design.

**Recommendation to evaluate:** Option 2. The package boundary is already clean. Keep trigger
engine and agent runtime as independent consumers of `internal/mcp`.

**Questions to resolve:**
- What are the failure modes if the MCP server is down during a poll check?
  Options: silent skip, log and alert, back-off retry. Recommendation: log + skip this cycle +
  exponential back-off after N consecutive failures.
- Does the poll check count against any rate limits on the MCP server? (Document as operator responsibility.)
- What exact format is the matched poll result delivered to the agent as the trigger payload?

---

### [DESIGN] ADR-015: Policy Concurrency Model

**Affects:** EPIC-008 (cron, poll, concurrency)

When multiple triggers fire for the same policy in quick succession, or a cron fires while a
previous run is still active, behavior must be explicitly defined.

**Options per policy (configurable in schema):**

| Mode | Behavior |
|---|---|
| `skip` | If a run is active, discard the new trigger silently. Safest. |
| `queue` | Runs execute sequentially. Configurable queue depth limit. |
| `parallel` | Runs execute concurrently up to a configured limit. |
| `replace` | Cancel the active run. Start fresh with the new trigger. Not valid if any actuator has `approval: required`. |

**Questions to resolve:**
- Which modes to support in v1? (Recommendation: all four — they are not complex individually.)
- What is the default if not configured? (Recommendation: `skip` — safest, least surprising.)
- Where is queue state held? (Recommendation: in-memory for v1, documented as lost on restart.
  Persisted queue state is a deferred enhancement.)
- Schema field name: `concurrency: skip | queue | parallel | replace`

---

### [DESIGN] ADR-016: MCP Tool Drift Handling

**Affects:** EPIC-009 (hardening and observability)

MCP servers can change — tools renamed, removed, new tools added — without Gleipnir knowing.
A policy that depends on a tool that has disappeared will fail at run start, but the operator
may not know why until they look at the run error.

**Questions to resolve:**
- How does Gleipnir detect drift?
  Options: manual re-discovery only (operator-triggered), periodic background check, or both.
- If a tool a policy depends on disappears, what is the behavior?
  Recommendation: validate at policy save time (non-blocking warn) AND validate at run start
  (blocking fail with a specific error naming the missing capability). Both are better than either alone.
- How are drift notifications surfaced in the UI?
- Should re-discovery show a diff of what changed before applying? (Recommendation: yes — auto-apply
  is dangerous if a tool rename silently breaks a policy.)

---

### [DESIGN] ADR-017: Security Model

**Affects:** EPIC-009 (hardening), EPIC-011 (deployment baseline)

Several distinct threat surfaces need explicit decisions before hardening work begins.

**Webhook authenticity:**
- HMAC secret storage: in DB (encrypted?) or env var? What is the rotation mechanism?
- Secret rotation: how does an operator rotate a webhook secret without downtime?
  Options: accept both old and new secret during a configurable overlap window.

**MCP server trust:**
- Gleipnir executes whatever tools the MCP server exposes. A compromised MCP server is a full
  capability bypass — Gleipnir cannot distinguish a legitimate tool from a malicious one.
  This is a deployment assumption, not a code-level mitigation. It must be documented explicitly.
- Should Gleipnir validate tool behavior in any way, or is trust fully delegated to the operator?
  (Recommendation: trust fully delegated. Document the trust boundary clearly in SECURITY.md.)

**Prompt injection via tool results:**
- Tool results are returned to the agent's context window. A malicious result could attempt to
  override instructions or manipulate the agent's subsequent tool calls.
- Options: document the risk only (v1), sanitize results, or wrap results in a structured format
  that reduces injection surface.
- What is the v1 stance? (Recommendation: document the risk. Structured wrapping is a deferred
  long-horizon enhancement.)

**Approval callback spoofing (if Slack callbacks are implemented per ADR-011):**
- HMAC verification on the callback endpoint is required before shipping Slack callbacks.

---

### [DESIGN] ADR-018: Feedback / Notification Channel Unification

**Affects:** EPIC-006 (approval gates), EPIC-010 (Slack integration)

The approval feedback channel (where the agent sends approval requests) and the run completion
notification channel are currently separate concepts in the policy schema. From an operator's
perspective, the ideal experience is a single thread: approval request → human responds →
run completes → summary posted to the same thread.

**Questions to resolve:**
- Should the feedback channel and notification channel be the same config, or separate?
- If unified: how does Gleipnir thread replies together in Slack (reply to original message using `thread_ts`)?
- If separate: does the completion notification reference the approval thread?
- What is the v1 behavior for the UI-only path?

---

## Milestone 1 — v0.1 MVP

**Goal:** A single webhook-triggered policy runs a BoundAgent against MCP tools and logs a full
reasoning trace. No approval gates. No auth. A demonstrable, end-to-end trigger → agent → audit loop.

**Definition of done for this milestone:** `docker-compose up` from a clean checkout produces a
working Gleipnir instance. A new user can register an MCP server, create a policy, trigger a webhook,
and view the reasoning trace in the UI — without asking for help.

**Milestone dependency:** Milestone 0 (all Phase 0 ADRs decided).

---

### EPIC-001: Data Model & Storage Layer

**Goal:** Establish the SQLite schema, data access layer, and core domain types that every other
component depends on. Nothing else can be built or tested without this foundation.

**Go packages produced:** `internal/db` (sqlc-generated), `internal/model` (domain types)

**Risks:**
- Schema changes after downstream epics begin require coordinated migrations. Get the core tables
  right before other epics start.
- The audit write queue must be tested under concurrent load. Serialized writes must not bottleneck
  run execution.

---

#### [BE] Initialize SQLite with WAL mode on startup
Enable WAL mode at the application layer on first connection (`PRAGMA journal_mode=WAL`).
Write a migration runner that applies sequential numbered `.sql` files on every boot.
Create the `schema_migrations` table to track applied versions and their timestamps.
_Labels: `backend`, `data-model` — No upstream dependencies._

#### [BE] Core schema: mcp_servers, mcp_tools
Create tables per `0001_initial_schema.sql`. ULID primary keys. ISO 8601 UTC timestamps throughout.
`mcp_tools.capability_role` enforced with a CHECK constraint: `sensor | actuator | feedback`.
Index on `mcp_tools.server_id`.
One capability role per tool (denormalized — the separate `capability_tags` join table design was
rejected; a join bought nothing given the 1:1 relationship).
_Labels: `backend`, `data-model` — Depends on: SQLite init._

#### [BE] Core schema: policies
`policies` table. `trigger_type` column with CHECK constraint: `webhook | cron | poll`.
`yaml` column stores the full policy YAML as the single source of truth.
`name` and `trigger_type` as indexed columns for fast list views and trigger routing.
Index on `trigger_type`.
_Labels: `backend`, `data-model` — Depends on: SQLite init._

#### [BE] Core schema: runs, run_steps
`runs` table with full status enum CHECK: `pending | running | waiting_for_approval | complete | failed | interrupted`.
`trigger_payload` stored as JSON blob — the webhook body / cron metadata / poll result that caused the run.
`thread_id` nullable — reserved for future Slack threading (written when first Slack message posts for a run).
`token_cost` INTEGER accumulates across all steps. Updated on each step write.
`error` nullable — populated on terminal `failed` or `interrupted` states only.

`run_steps` table: one row per agent conversation step. `step_number` is 0-indexed and contiguous within a run; step 0 is always `capability_snapshot`.
`type` CHECK: `thought | tool_call | tool_result | approval_request | feedback_request | feedback_response | error | complete`.
`content` is a raw JSON blob — shape varies by type (documented in schema comments). No typed columns.
`token_cost` is 0 for non-LLM steps.

Implement a serialized audit write queue at the application layer to prevent SQLite contention
under concurrent runs. Audit writes from multiple goroutines must be funneled through a single
channel-based writer goroutine.
_Labels: `backend`, `data-model` — Depends on: policies schema._

#### [BE] Core schema: approval_requests
`approval_requests` table.
`reasoning_summary` TEXT — a snapshot of the run's reasoning rendered at intercept time and stored here.
The approval UI reads from this snapshot rather than re-deriving it from `run_steps`.
`proposed_input` TEXT — JSON blob of the tool input the agent intended to pass.
`expires_at` TEXT — ISO 8601 UTC, computed from the policy's `approval_timeout` at creation time.
`status` CHECK: `pending | approved | rejected | timeout`.
`decided_at` nullable — set when status transitions from pending.
`note` nullable — operator comment on approve or reject.
_Labels: `backend`, `data-model` — Depends on: runs schema._

#### [BE] sqlc query definitions — all CRUD operations
Write named `.sql` query files for all tables. Run `sqlc generate`. Validate the generated Go types
compile and match the domain model.
Cover: insert, select by primary key, list with common filters (runs by policy_id, runs by status,
approval_requests by status), update status fields, delete.
_Labels: `backend`, `data-model` — Depends on: all schema issues._

#### [BE] Startup scan: mark orphaned runs as interrupted
On every boot, before accepting traffic: query for all runs with status `running` or `waiting_for_approval`.
For each: transition status to `interrupted`, write the `error` field with a clear message ("run interrupted
by server restart"), log the run ID and last known step number.
This scan is required for correctness of the reasoning timeline — orphaned runs must have a clean terminal state.
_Labels: `backend`, `data-model` — Depends on: sqlc queries._

#### [BE] Database migration strategy
Sequential numbered SQL files (e.g. `0001_initial_schema.sql`, `0002_add_foo.sql`).
On startup: read `schema_migrations`, compare against available migration files, apply any unapplied
migrations in order within a transaction. Fail startup if any migration fails.
_Labels: `backend`, `data-model` — Depends on: SQLite init._

#### [TEST] Table-driven tests: CRUD and concurrent audit writes
Cover: insert and select-by-ID for all tables, list queries with filters, status update transitions.
Concurrent audit writes: launch N goroutines each writing M `run_steps` rows for the same run through
the serialized queue. Verify: all rows written, `step_number` is contiguous and correct, no data loss,
no SQLite SQLITE_BUSY errors.
_Labels: `test`, `data-model` — Depends on: sqlc queries, audit write queue._

---

### EPIC-002: Policy Engine

**Goal:** Parse, validate, store, and retrieve policy definitions from YAML. The policy is the
central configuration primitive — it defines what an agent can do, what triggers it, and what
constraints apply.

**Go package produced:** `internal/policy` (parser, validator, renderer)

**Risks:**
- The YAML schema is the contract between the UI, trigger engine, and run executor. Changes after
  downstream epics begin require coordinated updates across all three.
- Template rendering must be injection-safe. User-provided template variables must not allow
  arbitrary code execution in the rendered output.

---

#### [BE] Policy YAML schema definition
Produce the canonical field reference for the policy YAML format. This is the schema contract.
All fields, their types, whether required or optional, and their defaults must be specified before
the parser is written.

Top-level sections:
- `name` (required, unique), `description` (optional)
- `trigger` block: `type: webhook | cron | poll`. Webhook has no additional fields. Cron requires
  `schedule` (5-field cron expression). Poll requires `interval` (Go duration string), `request`
  (url, method, optional headers and body with `${ENV_VAR}` substitution), and `filter` (JSONPath expression).
- `capabilities` block: `sensors` list, `actuators` list (each may have `approval: required`,
  `timeout`, `on_timeout`), `feedback` list (reserved, empty for v1).
  Tool references use dot notation: `server_name.tool_name`.
- `agent` block: `preamble` (optional multiline, defaults to Gleipnir's BoundAgent template),
  `task` (required multiline), `limits` (`max_tokens_per_run` default 20000, `max_tool_calls_per_run`
  default 50), `concurrency` (default `skip`).

_Labels: `backend`, `policy-engine` — No code dependency. Produces the schema contract used by all other EPIC-002 issues._

#### [BE] Policy YAML parser and structural validator
Parse a YAML string into a typed Go struct. Return structured validation errors — not a single
concatenated string. Each error should carry a field path and a human-readable message.

Validation rules:
- Required fields present and non-empty.
- `trigger.type` matches the available trigger config (e.g. `cron` type must have `schedule` field).
- Each tool reference in capabilities uses valid `server.tool` dot notation.
- `approval` config only valid on actuators, not sensors.
- `on_timeout` only valid when `approval: required`.
- `concurrency: replace` must not be set if any actuator has `approval: required` (schema-level block).
- Run limits are positive integers.
- `concurrency` is one of `skip | queue | parallel | replace`.
- Cron `schedule` is a valid 5-field expression (validate with robfig/cron parser).
- Poll `interval` is a parseable Go duration string with a minimum of 10s.
- Poll `filter` is a valid JSONPath expression (validate with the selected JSONPath library).

_Labels: `backend`, `policy-engine` — Depends on: policy schema definition._

#### [BE] Capability tag validation at save time (non-blocking warn)
At policy save: for each tool reference in capabilities, query the MCP registry for a matching
`server_name` + `tool_name` pair. If not found, add a warning to the save response.
Warnings do not block the save — operators legitimately save policies before registering MCP servers.
Return warnings in the API response body as a `warnings` array alongside the saved policy.
_Labels: `backend`, `policy-engine`, `mcp` — Depends on: policy parser, EPIC-003 tool registry._

#### [BE] Capability tag validation at run start (blocking fail)
At run dispatch (before launching the BoundAgent goroutine): re-validate all tool references
against the current MCP registry. If any tool reference resolves to zero registered tools,
fail the run immediately with status `failed` and an error message that names the specific
missing capability (e.g. "capability 'vikunja.task_get' not found in MCP registry").
Do not proceed to the conversation loop.
_Labels: `backend`, `policy-engine`, `agent-runtime` — Depends on: policy parser, EPIC-003 tool registry, EPIC-004 run executor._

#### [BE] System prompt template rendering
At run start, render the final system prompt from three components in order:
1. The policy's `preamble` field (or the default BoundAgent preamble if absent — see ADR-013).
2. A generated capabilities block listing sensor tool names under a "Sensors" heading and
   actuator tool names under an "Actuators" heading, rendered from the run's resolved tool set.
3. The feedback tool injection — a standard paragraph explaining the feedback tool is available
   and how to invoke it.

Then append the policy's `task` field as a separate section.

The rendered prompt is passed to the Claude API as the system message. It is not stored separately —
it is re-rendered at each run start from the policy YAML and the current tool registry state.
_Labels: `backend`, `policy-engine` — Depends on: policy parser._

#### [BE] Policy CRUD API endpoints
```
GET    /api/v1/policies           — list: name, trigger_type, last run status, last run time
GET    /api/v1/policies/:id       — full detail including YAML, last run summary
POST   /api/v1/policies           — create: parse + validate + store. Returns warnings array.
PUT    /api/v1/policies/:id       — replace YAML: re-parse + re-validate + store. Returns warnings array.
DELETE /api/v1/policies/:id       — remove. Fails with 409 if active runs exist for this policy.
```
All responses are JSON. YAML is returned as a string field, not parsed into nested JSON.
_Labels: `backend`, `policy-engine` — Depends on: policy parser, EPIC-001 data layer._

#### [TEST] Table-driven tests: policy parser and validator
Cover: valid webhook policy round-trip (parse → validate → store → retrieve → render),
valid cron policy, valid poll policy, missing required `name`, missing `task`, invalid `trigger.type`,
`concurrency: replace` with `approval: required` actuator (must reject), invalid cron schedule string,
unparseable poll interval, negative `max_tokens_per_run`, malformed YAML (not valid YAML at all),
valid policy with warnings (tool not in registry).
_Labels: `test`, `policy-engine` — Depends on: policy parser._

---

### EPIC-003: MCP Client & Tool Registry

**Goal:** Build the MCP HTTP client that discovers tools from registered MCP servers and
maintains a local registry with Gleipnir-specific capability tags. This is the bridge between
Gleipnir's policy model and the external tool ecosystem.

**Go package produced:** `internal/mcp` — must have no import dependencies on the agent runtime.
This is a hard package boundary: the poll trigger engine (EPIC-008) reuses this package
independently.

**Risks:**
- MCP servers are fully trusted. A compromised server can expose arbitrary tools. This is a
  documented deployment assumption (SECURITY.md), not a code-level mitigation.
- MCP server unavailability must produce clear errors, never silent failures.
- Tight coupling to the agent runtime now will require refactoring when the poll trigger
  engine needs this package. Keep the boundary clean from the start.

---

#### [BE] MCP HTTP client: connect and tool discovery
Go client that connects to an MCP server by URL, sends a `tools/list` JSON-RPC request,
and parses the response into a slice of tool structs (name, description, input schema as raw JSON).
Package: `internal/mcp`. No import of `internal/agent` or `internal/policy`.
Handle: successful response, server returns empty tool list, HTTP non-2xx, connection refused,
request timeout. Each failure mode returns a distinct typed error.
_Labels: `backend`, `mcp` — No upstream code dependencies._

#### [BE] MCP HTTP client: tool invocation
Send a `tools/call` JSON-RPC request to a specific server with the tool name and input JSON.
Return the tool result as a raw JSON value, or a typed error.
Failure modes must be distinct: server unreachable, server returned an error result,
request timeout, malformed response. The caller (BoundAgent runtime) must be able to distinguish
a tool-level error (the tool ran but returned an error) from a transport-level error (the call never reached the tool).
_Labels: `backend`, `mcp` — Depends on: MCP client connect._

#### [BE] MCP server registration API
```
POST   /api/v1/mcp-servers        — register: name, URL. Triggers initial discovery.
GET    /api/v1/mcp-servers        — list registered servers with connection status and last_discovered_at.
DELETE /api/v1/mcp-servers/:id    — remove server and cascade-delete its tools from the registry.
```
On registration: immediately attempt tool discovery and store results. If discovery fails, still
register the server but surface the discovery error in the response.
_Labels: `backend`, `mcp` — Depends on: MCP client tool discovery, EPIC-001 data layer._

#### [BE] Tool discovery: fetch and store tool list
On server registration and on manual re-discovery: call `tools/list`, upsert tool records into
`mcp_tools`. New tools: inserted with `capability_role` defaulting to `actuator` (safest default —
the operator explicitly assigns sensor/feedback roles). Removed tools: deleted. Modified tools
(schema change): updated.
_Labels: `backend`, `mcp` — Depends on: MCP client connect, server registration API._

#### [BE] Capability role assignment API
```
PATCH  /api/v1/mcp-tools/:id      — update capability_role: sensor | actuator | feedback
GET    /api/v1/mcp-tools          — list tools for a server with their current capability_role
```
Role assignment is manual — the operator decides which tools are sensors, actuators, or feedback
channels. Gleipnir cannot infer this from tool names or descriptions reliably.
_Labels: `backend`, `mcp` — Depends on: tool discovery._

#### [BE] Manual re-discovery endpoint
```
POST   /api/v1/mcp-servers/:id/rediscover         — fetch current tool list, compute diff, return without applying
POST   /api/v1/mcp-servers/:id/rediscover/apply   — apply the pending diff
```
The diff response shape: `{ "added": [...], "removed": [...], "modified": [...] }`.
Do not auto-apply on the first call. The operator reviews the diff before confirming.
This design prevents a tool rename on the MCP server silently breaking policies.
_Labels: `backend`, `mcp` — Depends on: tool discovery._

#### [TEST] Integration tests: MCP client against mock server
Use an in-process mock MCP HTTP server (not an external dependency).
Cover: `tools/list` returns tool list correctly, `tools/call` returns result, `tools/call` returns
tool-level error, server returns HTTP 500, server connection refused (server down), request timeout,
re-discovery detects added tool, re-discovery detects removed tool, re-discovery detects modified schema.
_Labels: `test`, `mcp` — Depends on: MCP client._

---

### EPIC-004: BoundAgent Runtime

**Goal:** The core agent execution engine. Takes a policy, assembles a filtered tool set,
runs a Claude API conversation loop, enforces run limits, and records a full reasoning trace.
This is the central component of Gleipnir.

**Go package produced:** `internal/agent` (BoundAgent runner, API loop, audit writer)

**No approval gate logic in this epic.** Actuators are called directly. Approval interception
is EPIC-006 and must not bleed into this epic.

**Risks:**
- A runaway agent that ignores stop signals could consume unbounded API tokens. The token budget
  and tool call cap are the primary blast-radius mitigations.
- Goroutine lifecycle management is critical. Leaked goroutines from cancelled runs will accumulate.
  Every code path that exits the conversation loop must cancel the context and release resources.
- Malicious content in MCP tool results enters the agent's context window (prompt injection risk).
  This is a documented risk for v1. Structured wrapping is a deferred enhancement.

---

#### [BE] BoundAgent runner: capability resolution and tool registration
Given a policy and its resolved capability tags, query the MCP registry to get the concrete
tool records (name, description, input schema) for each capability.
Register only those tools with the Claude API call — tools not in the capability list are
never passed to the Anthropic SDK. They do not exist in the agent's tool list.
This is the runtime enforcement of ADR-001. The capability envelope is assembled fresh on every run.
_Labels: `backend`, `agent-runtime` — Depends on: EPIC-002 policy engine, EPIC-003 MCP client._

#### [BE] Claude API conversation loop
Using the official Anthropic Go SDK:
1. Send the rendered system prompt (from EPIC-002) and the `trigger_payload` as the first user message.
2. Receive the model response.
3. For each `tool_use` block in the response: invoke the corresponding MCP tool via `internal/mcp`.
4. Collect all tool results and send them back as `tool_result` blocks.
5. Repeat from step 2 until the model returns `end_turn` (no tool calls), a run limit is exceeded,
   or the context is cancelled.
Write a `thought` step to `run_steps` for each text block in the model response.
Write a `tool_call` step before invoking each tool, and a `tool_result` step after.
_Labels: `backend`, `agent-runtime` — Depends on: capability resolution._

#### [BE] Run limit enforcement: token budget
Track cumulative token cost (input tokens + output tokens) across all API calls in a run.
Sum from the `usage` field in each Anthropic API response. After each API call, check against
`max_tokens_per_run`. If exceeded: write an `error` step, transition the run to `failed`,
return a clear error message: "token budget exceeded: {actual} tokens used, limit {limit}".
_Labels: `backend`, `agent-runtime` — Depends on: Claude API loop._

#### [BE] Run limit enforcement: tool call cap
Track cumulative tool invocations across the run. After each tool call, check against
`max_tool_calls_per_run`. If exceeded: write an `error` step, transition to `failed`,
return a clear error: "tool call limit exceeded: {actual} calls, limit {limit}".
_Labels: `backend`, `agent-runtime` — Depends on: Claude API loop._

#### [BE] Audit logging: run_steps writer
Write each agent step to `run_steps` via the serialized audit queue (EPIC-001).
Step types and their content shapes:
- `thought`: `{ "text": "..." }` — from model text blocks. `token_cost` = tokens for this response.
- `tool_call`: `{ "tool_name": "...", "server_id": "...", "input": {...} }`. `token_cost` = 0.
- `tool_result`: `{ "tool_name": "...", "output": ..., "is_error": false }`. `token_cost` = 0.
- `error`: `{ "message": "...", "code": "..." }`. `token_cost` = 0.
- `complete`: `{ "summary": "..." }`. `token_cost` = 0.
Accumulate `token_cost` into `runs.token_cost` on each write. `step_number` is 0-indexed,
incremented atomically within the run.
_Labels: `backend`, `agent-runtime` — Depends on: EPIC-001 audit write queue, Claude API loop._

#### [BE] Run state machine: DB-persisted transitions
Every run state transition must be written to the DB before the code proceeds to the next state.
This is required for the startup scan (EPIC-001) to correctly identify and clean up orphaned runs.
Transitions: `pending → running` (at goroutine launch), `running → complete` (normal exit),
`running → failed` (limit exceeded, tool error, capability missing), `running → waiting_for_approval`
(EPIC-006), `waiting_for_approval → running` (EPIC-006), `waiting_for_approval → failed` (EPIC-006).
Set `completed_at` on all terminal transitions.
_Labels: `backend`, `agent-runtime` — Depends on: audit logging._

#### [BE] Context-based cancellation
Runs execute as goroutines with a `context.Context` passed through the entire call stack:
capability resolution, Claude API calls, MCP tool invocations.
On context cancellation: the current operation returns an error, the conversation loop exits cleanly,
the run transitions to `failed` with a cancellation error message, the goroutine exits.
No goroutine should remain running after its context is cancelled. Verify with leak-detection tests.
_Labels: `backend`, `agent-runtime` — Depends on: run state machine._

#### [BE] Fail-fast on missing tools at run start
Before entering the conversation loop, verify each capability tag from the policy resolves to
a registered tool in the MCP registry. If any tag has no match: immediately transition the run
to `failed`, write an error step naming the missing capability, do not enter the loop.
This check is in addition to the save-time warning (EPIC-002). The MCP registry can change
between policy save and run start.
_Labels: `backend`, `agent-runtime` — Depends on: capability resolution._

#### [TEST] Table-driven tests: BoundAgent runner
Use a mock Anthropic SDK client and a mock MCP server for all tests. No live API calls.
Cover: normal run completes with `complete` step and full trace, token budget exceeded (terminates
cleanly with `failed`), tool call cap exceeded (terminates cleanly), missing tool at run start
(fails before loop entry with descriptive error naming the missing capability), context cancellation
mid-loop (goroutine exits, no leak, run marked `failed`), MCP tool invocation error (run records
error step and continues or fails — define policy), multi-tool-call loop (verify step_number
increments correctly and all steps are written).
_Labels: `test`, `agent-runtime` — Depends on: BoundAgent runner._

---

### EPIC-005: Webhook Trigger & Run Orchestration

**Goal:** The HTTP endpoint that receives external events and dispatches agent runs. This is the
entry point for the v0.1 trigger → agent → audit loop.

**No HMAC verification in v0.1** (deferred to EPIC-009). This is a known gap — document it in SECURITY.md.
**No concurrency control in v0.1** (deferred to EPIC-008). Concurrent webhook fires for the same
policy both run — document the risk.

---

#### [BE] Webhook HTTP handler
```
POST /api/v1/webhooks/:policy_id
```
Receives any JSON body. Validates the policy exists (404 if not). Creates a `runs` record in
`pending` state with `trigger_type: webhook` and the raw request body as `trigger_payload`.
Launches the BoundAgent runner (EPIC-004) as a goroutine. Returns `202 Accepted` with the run ID.
The handler returns immediately — it does not wait for the run to complete.
_Labels: `backend`, `triggers` — Depends on: EPIC-004 BoundAgent runtime, EPIC-001 data layer._

#### [BE] Run list and detail API endpoints
```
GET  /api/v1/runs                  — list: status, trigger_type, trigger_time, duration, token_cost
                                     Query params: policy_id, status, limit, offset
GET  /api/v1/runs/:id              — full run detail including policy snapshot
GET  /api/v1/runs/:id/steps        — ordered run_steps for the reasoning timeline
```
_Labels: `backend`, `triggers` — Depends on: EPIC-001 data layer._

#### [BE] Run cancellation endpoint
```
POST /api/v1/runs/:id/cancel
```
Cancels an active run by cancelling its context. Only valid for runs in `running` state.
Returns 409 if the run is not in a cancellable state.
_Labels: `backend`, `triggers` — Depends on: EPIC-004 context cancellation._

#### [TEST] Integration test: webhook → run → completion
POST to webhook URL with a JSON body. Assert: 202 response with run ID, run exists in DB with
`pending` then `running` then `complete` states, reasoning trace written to `run_steps` with
correct step types and contiguous step numbers, `token_cost` accumulated on the run record.
Two rapid webhook calls for the same policy: both execute to completion — no crash, no deadlock,
both runs have separate complete traces.
_Labels: `test`, `triggers` — Depends on: webhook handler._

---

### EPIC-011: Deployment, Documentation & Security Baseline

**Goal:** Package Gleipnir for deployment, document setup and operational procedures, and
establish the v0.1 security baseline. After this epic, a new operator can go from zero to a
running Gleipnir instance by following the README.

---

#### [BE] Dockerfile: Go API container
Multi-stage build: build stage uses the full Go toolchain, final stage is a minimal image.
Copies only the compiled binary. SQLite data directory configurable via env var (`GLEIPNIR_DB_PATH`).
Optimized for build cache (dependency download as a separate layer).
_Labels: `backend`, `docs` — Depends on: all v0.1 epics._

#### [BE] docker-compose.yml
Services: `gleipnir-api` (Go binary, serves both API and embedded React UI).
SQLite data as a named Docker volume mounted into the API container.
Configurable port via env var.
_Labels: `backend`, `docs` — Depends on: Dockerfiles._

#### [BE] .env.example
Document every configurable environment variable with inline comments:
`ANTHROPIC_API_KEY`, `GLEIPNIR_DB_PATH`, `GLEIPNIR_PORT`, `GLEIPNIR_AUTH_USER`,
`GLEIPNIR_AUTH_PASS` (optional), `GLEIPNIR_LOG_LEVEL`.
_Labels: `backend`, `docs` — Depends on: all v0.1 epics._

#### [BE] SECURITY.md: trust model and known gaps
Three explicit sections:

**MCP server trust:** MCP servers are fully trusted by Gleipnir. A compromised MCP server can
expose arbitrary tools to the agent, bypassing the capability policy defined in a run. Operators
must treat MCP server containers as part of their security trust boundary. Gleipnir does not
validate tool behavior.

**Webhook exposure:** Webhook endpoints in v0.1 have no signature verification. The webhook URL
is effectively a secret — anyone who knows it can trigger runs. Treat it as a credential.
HMAC verification is planned for v0.4.

**Prompt injection:** Tool results returned from MCP servers enter the agent's context window.
A malicious tool result could attempt to manipulate the agent's subsequent reasoning or tool calls.
This is a known risk in v0.1. Structured result wrapping is a deferred enhancement.
_Labels: `docs`, `security` — No code dependency._

#### [BE] Operational runbook in README
Sections: prerequisites, `docker-compose up`, registering an MCP server, creating a first policy,
triggering a webhook, viewing a reasoning trace. How to back up the SQLite database. How to view
structured logs. How to reset a stuck run (direct DB update procedure with warning).
_Labels: `docs` — Depends on: all v0.1 epics._

---

## Milestone 2 — v0.2 Human-in-the-Loop

**Goal:** Approval gates working end-to-end. A run suspends before an approval-gated actuator call,
a human approves or rejects from the Gleipnir UI, and the run resumes or fails cleanly.

**Milestone dependency:** Milestone 1 (v0.1 MVP complete).

---

### EPIC-006: Approval Gates & Human-in-the-Loop

**Goal:** Add the approval interception layer — policy-gated actuators pause the run, surface an
approval request in the UI, wait for a human decision, and resume or reject. Also add the
agent-initiated feedback tool for voluntary human consultation.

**Go packages affected:** `internal/agent` (interceptor), `internal/approval`, `internal/notify`
(kept as separate packages — approval logic and notification delivery are distinct concerns).

**Risks:**
- A run suspended in `waiting_for_approval` holds a goroutine. If many runs are paused simultaneously,
  goroutine count grows. The timeout mechanism is the primary pressure valve — keep it working.
- If Gleipnir restarts while a run is waiting for approval, the startup scan (EPIC-001) marks it
  `interrupted`. The human's approval window is lost. This is acceptable for v0.2.
- The agent may not use the feedback tool even when available — it is encouraged via system prompt,
  not enforced. Policy-gated approval is the hard guarantee. Agent-initiated feedback is a quality
  improvement, not a safety mechanism.

---

#### [BE] Approval interceptor: pre-actuator interception
In the BoundAgent conversation loop (EPIC-004), before executing any actuator tool call: check
whether the corresponding policy capability has `approval: required`. If yes:
1. Render a `reasoning_summary` from the most recent N `run_steps` (thoughts and tool calls).
2. Create an `approval_requests` record with: `tool_name`, `proposed_input` (the exact input the
   agent passed), `reasoning_summary`, `status: pending`, `expires_at` computed from the
   policy's timeout config for this actuator.
3. Write an `approval_request` step to `run_steps` referencing the `approval_request_id`.
4. Transition the run to `waiting_for_approval`.
5. Block the goroutine on a channel or condition variable — do not poll the DB in a tight loop.
_Labels: `backend`, `approval` — Depends on: EPIC-004 BoundAgent runtime, EPIC-001 data layer._

#### [BE] Run state: waiting_for_approval transitions
Implement the `waiting_for_approval` state in the run state machine.
Transitions: `running → waiting_for_approval` (interceptor), `waiting_for_approval → running`
(approved — unblock the goroutine, continue the loop, execute the tool),
`waiting_for_approval → failed` (rejected — unblock the goroutine, write a `complete` step
with rejection reason, terminate).
The goroutine must wake cleanly on both approval and rejection signals without polling.
_Labels: `backend`, `approval` — Depends on: approval interceptor._

#### [BE] Approval timeout: background scanner
A background goroutine that runs on a configurable tick (e.g. every 30s). Queries for
`approval_requests` where `status = pending` and `expires_at < now`.
For each expired request: transition `approval_requests.status` to `timeout`, wake the blocked
run goroutine with a rejection signal. The run transitions to `failed` with the reason
"approval timeout: {tool_name} not approved within {timeout duration}".
`on_timeout: reject` is the default. `on_timeout: approve` (auto-approve on timeout) is
supported if configured in the policy actuator config.
_Labels: `backend`, `approval` — Depends on: waiting_for_approval state._

#### [BE] Approval decision API endpoints
```
GET    /api/v1/approval-requests              — list pending requests (most recent first)
GET    /api/v1/approval-requests/:id          — detail: tool_name, proposed_input, reasoning_summary, expires_at
POST   /api/v1/approval-requests/:id/approve  — approve with optional { "note": "..." }
POST   /api/v1/approval-requests/:id/reject   — reject with optional { "note": "..." }
```
Approve/reject: validate request exists and status is still `pending` (reject with 409 if
already decided or timed out). Set `status`, `decided_at`, `note`. Signal the blocked goroutine.
_Labels: `backend`, `approval` — Depends on: approval interceptor._

#### [BE] Agent-initiated feedback tool
Register a special pseudo-tool with the BoundAgent at run start (alongside the MCP tools).
Tool name: `gleipnir_feedback`. Input schema: `{ "message": "string" }`.
When the agent calls it:
1. Write a `feedback_request` step to `run_steps` with the agent's message.
2. Deliver the message via the policy's feedback channel (UI for v0.2).
3. Block the goroutine waiting for a human response.
4. On response: write a `feedback_response` step, return the response to the agent as the tool result.
The agent sees this as an ordinary tool call/result pair. The delivery mechanism is transparent to the agent.
_Labels: `backend`, `approval` — Depends on: EPIC-004 BoundAgent runtime._

#### [BE] Feedback response API endpoint
```
POST /api/v1/runs/:id/feedback-response   — body: { "response": "..." }
```
Valid only when the run has an active `feedback_request` step with no corresponding `feedback_response`.
Stores the response and signals the blocked goroutine.
_Labels: `backend`, `approval` — Depends on: agent-initiated feedback tool._

#### [TEST] Approval flow integration tests
Cover: actuator with `approval: required` — run transitions to `waiting_for_approval` and blocks,
approve → run resumes → tool executes → run completes, reject → run fails with rejection reason,
timeout with `on_timeout: reject` → run fails with timeout reason, timeout with `on_timeout: approve`
→ run continues, feedback tool round-trip (agent sends message → UI receives it → human responds →
agent receives response as tool result), restart while waiting (startup scan marks `interrupted`).
_Labels: `test`, `approval` — Depends on: approval decision API._

---

## Milestone 3 — v0.3 Full Trigger Support

**Goal:** All three trigger types operational (webhook was v0.1). Gleipnir can run autonomously
on a schedule or in response to detected state changes, with predictable concurrency behavior.

**Milestone dependency:** Milestone 1 (v0.1 MVP). Milestone 2 (v0.2) needed for replace-mode constraint.

---

### EPIC-008: Cron, Poll & Concurrency

**Goal:** Add cron and poll trigger types and per-policy concurrency control so overlapping
triggers are handled predictably.

**Go package produced:** `internal/trigger` (cron scheduler, poll engine, concurrency manager)

**Risks:**
- Cron schedules that fire every minute with a slow agent run will generate constant skip/queue
  events. Make this visible in the UI — operators need to tune their schedules.
- Poll trigger failures against a flaky MCP server can generate excessive retries.
  Back-off and a max-retry count are essential.
- Queue state held in-memory is lost on restart. Document this. Persisted queue is a deferred
  long-horizon feature.
- `replace` mode interacts with approval gates. A run waiting for approval cannot be safely
  replaced without cancelling the pending approval request. Handle this explicitly.

---

#### [BE] Cron trigger: robfig/cron integration
Policies with `trigger.type: cron` register their schedule with a `robfig/cron` scheduler
at startup. When the cron fires, dispatch a run via the same run orchestration path as webhooks.
The `trigger_payload` for a cron run is a JSON object with metadata: `{ "trigger_type": "cron", "fired_at": "..." }`.
Use the cron scheduler's `AddFunc` / `Remove` API for lifecycle management.
_Labels: `backend`, `triggers` — Depends on: EPIC-005 run orchestration._

#### [BE] Cron trigger: live schedule updates
On policy update: if `trigger.type: cron`, remove the old cron entry by entry ID and register
the new schedule. The transition must be atomic — no window where neither the old nor the new
schedule is registered, and no window where both are registered.
On policy delete: deregister the cron entry.
_Labels: `backend`, `triggers` — Depends on: cron integration._

#### [BE] Poll trigger: HTTP polling engine
For each policy with `trigger.type: poll`, run a background loop on the configured `interval`.
On each tick:
1. Perform `${ENV_VAR}` substitution in the request URL, headers, and body from the process environment.
   Missing env var → fail this poll cycle with a logged error, continue to next interval.
2. Make the HTTP request. Non-2xx → treat as poll failure.
3. Parse the response body as JSON. JSONPath evaluation error → treat as poll failure.
4. Evaluate the `filter` JSONPath expression against the response.
5. If the result is non-empty: dispatch a run. The matched result becomes the `trigger_payload`.
6. If the result is empty: no run dispatched. Log at debug level.
_Labels: `backend`, `triggers` — Depends on: EPIC-005 run orchestration. Design per ADR-014._

#### [BE] Poll trigger: failure handling and back-off
Track consecutive failure count per poll policy. On each failure: log the error with the policy ID,
URL, and reason. Increment consecutive failure count. Apply exponential back-off with a configurable
cap (e.g. max back-off interval = 10× the configured `interval`, hard ceiling of 1 hour).
On a successful poll: reset consecutive failure count and back-off to the configured interval.
Surface failure state and consecutive failure count in `GET /api/v1/policies/:id` response.
_Labels: `backend`, `triggers` — Depends on: poll trigger engine._

#### [BE] Concurrency control: skip mode (default)
When a trigger fires for a policy that already has a run in `running` or `waiting_for_approval`
state: discard the new trigger. Log the skip event at info level with the policy ID and trigger source.
This is the default if `concurrency` is not specified in the policy YAML.
_Labels: `backend`, `triggers` — Depends on: cron + poll trigger, EPIC-005 run orchestration._

#### [BE] Concurrency control: queue mode
When a trigger fires and a run is active: enqueue the trigger payload (not a full run record —
just the payload and trigger metadata). When the active run reaches a terminal state, dequeue
the next trigger and dispatch a new run.
Configurable `queue_depth` limit in the policy YAML (default: 10). If queue is full at enqueue
time: discard the new trigger and log the overflow.
Queue state is in-memory. It is lost on restart. Document this limitation.
_Labels: `backend`, `triggers` — Depends on: skip mode._

#### [BE] Concurrency control: parallel mode
Allow concurrent runs for a policy up to a configured `max_parallel` limit (default: 3).
If at the limit when a trigger fires: discard the new trigger (same behavior as skip).
Track active run count per policy in-memory. Decrement on run terminal state.
_Labels: `backend`, `triggers` — Depends on: skip mode._

#### [BE] Concurrency control: replace mode
When a trigger fires and a run is active: cancel the active run (context cancellation).
Wait for the goroutine to exit cleanly. Then dispatch a new run with the new trigger payload.
Constraint: `replace` mode must be blocked at the schema validator level if any actuator in the
policy has `approval: required`. This constraint is enforced in EPIC-002's parser — this issue
verifies the runtime behavior matches the schema constraint, and adds clean cancellation of any
pending approval request (set `status: timeout`, wake the goroutine with a cancellation signal)
before cancelling the run context.
_Labels: `backend`, `triggers` — Depends on: skip mode, EPIC-006 approval gates._

#### [BE] Update startup scan for cron/poll runs
Extend the EPIC-001 startup scan to explicitly handle the fact that `interrupted` runs may be
cron- or poll-initiated. No behavior change needed — the scan already catches all `running` and
`waiting_for_approval` runs regardless of trigger type. Verify this in tests.
Update `last_fired_at` tracking: on startup, do not reset `last_fired_at` for cron policies —
preserve the last-known fire time so cron miss detection (EPIC-009) has accurate data.
_Labels: `backend`, `triggers` — Depends on: EPIC-001 startup scan._

#### [TEST] Concurrency and trigger tests
Cover: cron fires on schedule and dispatches a run, live cron schedule update takes effect
without restart, poll filter match fires run with matched payload as trigger_payload,
poll filter no-match produces no run, poll HTTP failure increments failure count and applies
back-off, consecutive failures reach back-off cap and stop growing, back-off resets on success,
skip mode discards second trigger while first run is active, queue mode executes triggers in order,
queue depth overflow discards trigger, parallel mode runs up to limit and discards at limit,
replace mode cancels active run cleanly and starts new run, replace mode with pending approval
request cancels approval then cancels run.
_Labels: `test`, `triggers` — Depends on: all concurrency modes._

---

## Milestone 4 — v0.4 Hardening & Observability

**Goal:** Gleipnir is reliable enough to run unattended. Failures are visible to the operator.
Security surfaces are hardened. After this milestone, an operator can leave Gleipnir running
and trust it to either work correctly or fail visibly.

**Milestone dependency:** Milestones 1–3 complete.

---

### EPIC-009: Hardening & Observability

**Go packages affected:** hardening changes distributed across `internal/api`, `internal/agent`,
`internal/trigger`, `internal/mcp`, `internal/notify`.

**Risks:**
- Adding HMAC verification changes the webhook contract. Existing integrations will break if
  the rollout is not coordinated with documentation updates.
- Drift detection that auto-applies changes could silently break policies.
  The diff-before-apply UX is not optional.

---

#### [BE] Health endpoint
```
GET /api/v1/health
```
Returns structured JSON with status of: SQLite (reachable + last write latency), each registered
MCP server (reachable: last successful `tools/list` response time), cron scheduler (running: number
of registered schedules). Overall `status: ok | degraded | unhealthy`.
Suitable for Docker `HEALTHCHECK` — returns HTTP 200 when ok/degraded, 503 when unhealthy.
_Labels: `backend`, `observability` — Depends on: EPIC-003 MCP client, EPIC-008 cron._

#### [BE] MCP server unreachable handling during run
If an MCP tool invocation fails due to server unreachability (connection refused, DNS failure,
HTTP 5xx): fail the run immediately. Write an `error` step with a clear message naming the server
and tool. Do not silently retry during a run. The operator should see exactly which MCP server
caused the failure.
This is a behavior hardening of the existing EPIC-004 error handling — verify the error messages
are operator-actionable, not generic timeouts.
_Labels: `backend`, `observability`, `mcp` — Depends on: EPIC-004 BoundAgent runtime._

#### [BE] Cron miss detection
Store `last_fired_at` (ISO 8601 UTC) per policy. Update on every successful cron dispatch.
Surface `last_fired_at` in `GET /api/v1/policies/:id` and `GET /api/v1/policies`.
Alert mechanism: after N consecutive missed expected fires (configurable env var, default N=3),
emit a structured log entry at `WARN` level with policy ID, schedule, and expected fire time.
Optional: if `GLEIPNIR_ALERT_WEBHOOK` is configured, POST a JSON alert payload to that URL.
_Labels: `backend`, `observability` — Depends on: EPIC-008 cron trigger._

#### [BE] Webhook HMAC verification
Per-policy HMAC-SHA256 secret. Storage: in the DB as an encrypted field, or as an env var
reference (operator provides `GLEIPNIR_WEBHOOK_SECRET_<POLICY_ID>`). Decide per ADR-017.
On inbound webhook: validate the `X-Gleipnir-Signature-256` header (format: `sha256=<hex>`).
If the header is missing when HMAC is configured for the policy: reject with 401.
If the signature is invalid: reject with 401. Log the rejection with policy ID and source IP.

Secret rotation: accept both the current and a previous secret during a configurable overlap
window (`GLEIPNIR_WEBHOOK_SECRET_ROTATION_WINDOW`, default 1h). After the window, the old
secret is invalid.

Secret management API:
```
POST /api/v1/policies/:id/webhook-secret/rotate — generate new secret, begin rotation window
GET  /api/v1/policies/:id/webhook-secret        — returns the current secret (never the previous)
```
_Labels: `backend`, `security` — Depends on: EPIC-005 webhook handler, ADR-017._

#### [BE] Basic auth middleware
Optional HTTP Basic Auth protecting all `/api/v1` routes and the frontend.
Activated when both `GLEIPNIR_AUTH_USER` and `GLEIPNIR_AUTH_PASS` env vars are set.
If not configured: all routes are unauthenticated (appropriate for single-operator homelab
deployments on a private network).
Return `WWW-Authenticate: Basic realm="Gleipnir"` header on 401.
_Labels: `backend`, `security` — Depends on: all API endpoints._

#### [BE] Feedback / approval delivery failure handling
If the feedback channel fails to deliver an approval request or feedback message (for v0.4,
UI delivery is in-process and should not fail — this primarily targets Slack in v0.5), retry
the delivery with exponential back-off up to 3 attempts.
If all retries exhausted: write an `error` step to `run_steps`, transition the run to `failed`
with the reason "feedback delivery failed after {N} attempts: {error}".
No dead-letter queue in v0.4.
_Labels: `backend`, `approval` — Depends on: EPIC-006 feedback channel._

#### [BE] Policy validation completeness review
Audit EPIC-002's save-time validation and EPIC-004's run-start validation against the v0.4
feature set. Verify: concurrency mode interactions are caught (e.g. `replace` + `approval: required`),
poll trigger config is fully validated, all approval timeout configurations are validated,
env var references in poll request config are flagged as warnings at save time if the var is
not currently set.
_Labels: `backend`, `policy-engine`, `observability` — Depends on: EPIC-002, EPIC-004, EPIC-008._

#### [TEST] Hardening integration tests
Cover: GET /health returns correct status when all services up, returns degraded when one MCP
server unreachable, returns unhealthy when SQLite unavailable. HMAC verification: valid signature
accepted, invalid signature rejected with 401, missing signature rejected with 401, rotation window
accepts old and new secret, after window old secret rejected. Basic auth: request with correct
credentials succeeds, request without credentials returns 401 with WWW-Authenticate header.
Cron miss: policy misses N fires, WARN log emitted.
_Labels: `test`, `observability`, `security` — Depends on: all EPIC-009 issues._

---

## Milestone 5 — v0.5 Slack Integration

**Goal:** Approval requests and run completion notifications routed through Slack. The operator
can receive context-rich messages in Slack and approve from the Gleipnir UI (primary path) or
directly from Slack via interactive buttons (secondary path, requires tunnel setup).

**Milestone dependency:** Milestone 2 (v0.2 Human-in-the-Loop complete).

---

### EPIC-010: Slack Integration

**Go package produced:** `internal/notify/slack`

**Risks:**
- Slack API rate limits could delay approval message delivery for operators with many concurrent runs.
  The per-policy thread structure partially mitigates this — each policy's run activity is isolated.
- Tunnel setup (ngrok, Cloudflare Tunnel) adds deployment complexity. The UI-only fallback must
  always work regardless of whether Slack callbacks are configured.
- Slack Block Kit message formatting varies across clients. Test on desktop and mobile.

---

#### [BE] Slack feedback channel: outbound posting
Implement the Slack feedback channel delivery backend in `internal/notify/slack`.
On approval request creation: post a structured Slack Block Kit message to the configured channel.
Message content: policy name, tool name, proposed input (formatted as a code block), reasoning summary.
Include a link to the Gleipnir UI approval detail page as the primary action path.
Write the returned Slack `thread_ts` to `runs.thread_id`.
_Labels: `backend`, `slack` — Depends on: EPIC-006 approval interceptor, EPIC-001 thread_id column._

#### [BE] Slack threading: all run messages as thread replies
All subsequent Slack messages for a run (additional approval requests, feedback messages,
completion notification) are posted as thread replies using the `thread_ts` stored in `runs.thread_id`.
If `thread_id` is null when a message is to be sent (first message for this run): post as a new
message and write the returned `thread_ts` to `runs.thread_id`.
_Labels: `backend`, `slack` — Depends on: Slack outbound posting._

#### [BE] Slack completion notifications
When a run reaches a terminal state (`complete`, `failed`, or `interrupted`): post a completion
summary to the run's Slack thread.
Content: outcome label, total duration, total token cost, brief outcome summary (the `summary`
field from the `complete` step, or the error message from the `error` step).
_Labels: `backend`, `slack` — Depends on: Slack threading._

#### [BE] Feedback channel resolution: policy-level Slack config
Extend the policy YAML schema to support a Slack feedback channel config block:
```yaml
feedback_channel:
  slack:
    channel: "#ops-approvals"
```
Resolution order (ADR-009): policy-level Slack config → system-level Slack config
(from env vars `GLEIPNIR_SLACK_CHANNEL`, `GLEIPNIR_SLACK_TOKEN`) → UI only (fallback).
_Labels: `backend`, `slack`, `policy-engine` — Depends on: Slack outbound posting._

#### [BE] Slack interactive callbacks (secondary approval path)
Implement inbound callback endpoint:
```
POST /api/v1/slack/callback
```
Receives Slack interactive component payloads (approve/reject button clicks).
Mandatory HMAC verification using the Slack signing secret (`GLEIPNIR_SLACK_SIGNING_SECRET` env var)
before any processing. Invalid signature → 401.
On valid approve payload: call the same approval logic as `POST /api/v1/approval-requests/:id/approve`.
On valid reject payload: call the same rejection logic.
Requires the Gleipnir instance to be publicly reachable via a configured callback URL. See tunnel documentation.
_Labels: `backend`, `slack`, `security` — Depends on: Slack outbound posting, EPIC-006 approval decision API._

#### [BE] Slack tunnel setup documentation
Document step-by-step setup for both ngrok and Cloudflare Tunnel to make `/api/v1/slack/callback`
reachable from Slack's servers. Include: callback URL format, Slack app configuration (Request URL field),
signing secret retrieval, how to verify the callback is working.
Explicitly note that the UI approval path works without any tunnel configuration — the tunnel is
only required for interactive button approvals.
_Labels: `docs`, `slack` — No code dependency._

#### [TEST] Slack integration tests
Use a mock Slack API server for all tests — no live Slack API calls.
Cover: approval request posts message to correct channel, `thread_ts` written to `runs.thread_id`,
subsequent messages posted as thread replies, completion notification posts to thread,
interactive callback with valid HMAC approves run, interactive callback with invalid HMAC rejected with 401,
policy-level channel overrides system-level channel.
_Labels: `test`, `slack` — Depends on: all EPIC-010 issues._

---

## Milestone 6 — Long Horizon (Deferred)

> Items deliberately out of scope until a concrete use case and milestone are assigned.
> Create as GitHub issues with the `deferred` label and no milestone.
> Revisit dates are noted — promote to an active milestone when the stated preconditions are met.

---

### Multi-user Support

#### [FUTURE] Multi-user RBAC and identity model
Per-user authentication with role model: `admin`, `operator`, `viewer`.
Approval requests attributed to the approving user identity (stored in `approval_requests`).
Per-user approval routing: certain policies can require approval from a specific user or role.
_Revisit: after v0.4 is stable and there is evidence of multi-operator use._

#### [FUTURE] OIDC / SSO integration
Support external identity providers (Okta, Auth0, Google Workspace) for authentication.
Replace basic auth with OIDC authorization code flow.
_Revisit: after multi-user RBAC model is designed (ADR-011 stub)._

---

### Agent-to-Agent Coordination

#### [FUTURE] Agent-to-agent spawning — Council Agent pattern
A BoundAgent can spawn a child agent with a narrower, explicitly granted capability envelope.
The parent agent passes the child a task description and a subset of its own capabilities.
The feedback channel becomes the inter-agent communication channel.
The spawning agent can approve or reject certain child action classes on behalf of a human,
acting as a programmatic approver for low-risk decisions.
_Revisit: after multi-user and policy templates are stable (ADR-012 stub)._

---

### Run Context & Memory

#### [FUTURE] Run summary and embedding strategy
Compute a vector embedding of the `complete` step's `summary` field for each run.
Store embeddings in SQLite (via sqlite-vec or a sidecar vector store).
Enable a future agent to retrieve semantically similar past runs as context before starting a new run.
_Revisit: when observability and run volume make this tractable (ADR-013 stub)._

#### [FUTURE] RAG-based run context injection
Use run embeddings to automatically inject relevant past-run summaries into the agent's system
prompt as context. Operator configures `context_k` (number of past runs to inject) in the policy.
_Revisit: after run embedding strategy is implemented._

---

### Policy Quality & Testing

#### [FUTURE] Policy dry-run mode
Run the agent with full sensor access but intercept all actuator calls — log what it would have
done without executing anything. Useful for safely testing a new policy before enabling it in production.
The dry-run mode produces a complete reasoning trace marked `dry_run: true`.
_Revisit: v0.4 or v0.5._

#### [FUTURE] Policy mutation testing
Automated framework for testing policy behavior against a library of synthetic trigger payloads.
Assert on: which actuators were called, which approval gates were triggered, whether the run
completed or failed, token cost. Useful for regression testing policy changes.
_Revisit: after policy templates are stable._

#### [FUTURE] Shadow runs — The Witness
Run a second agent instance in parallel with the primary agent, configured with sensors only
(no actuators). The Witness produces its own reasoning trace. Compare the primary agent's action
plan against The Witness's observations to detect unexpected behavior or policy drift.
_Revisit: after v0.5, when there is sufficient run volume to make comparisons meaningful._

#### [FUTURE] Policy genealogy and cross-policy dependency mapping
Track which policies share tool dependencies. When a tool is modified or removed from the MCP
registry, surface which policies are affected across the entire policy set — not just the one
being validated.
_Revisit: when operators have enough policies (10+) to need this._

---

### Approval & Notification Enhancements

#### [FUTURE] Conditional auto-approval
A third approval mode alongside agent-initiated and policy-gated. Evaluate JSONPath conditions
against the proposed tool input. If all conditions pass, execute without a human gate. If any
condition fails, escalate to the normal policy-gated flow.
Example: auto-approve `kubectl.scale` if `input.replicas <= 3`, require approval otherwise.
This was captured as a Phase 0 design question — it should become an ADR and a EPIC-006 extension.
_Revisit: after v0.2 approval gates have real usage data._

#### [FUTURE] Approval pattern analytics
Track approval decisions over time per actuator per policy.
Surface: approval rate, average decision latency, timeout frequency, most-approved inputs.
Useful for identifying candidates for conditional auto-approval.
_Revisit: after v0.5 with meaningful usage data._

#### [FUTURE] Automated escalation ladders
If an approval request is not decided within a configurable first-tier window, escalate:
post to a different Slack channel, page a different user, or increase urgency indicators.
Requires multi-user support to be meaningful.
_Revisit: after multi-user support._

---

### Infrastructure

#### [FUTURE] Resumable runs after restart
Fully serialize the Claude API conversation history (all messages sent and received) to the DB
at each step. On startup, if a run was `interrupted`, offer the operator the ability to resume:
re-hydrate the goroutine with the full conversation history and continue from the last step.
This requires significant changes to the run state machine and the audit log schema.
_Revisit: v0.4, or when operators report restart-related data loss as a significant pain point._

#### [FUTURE] Persisted queue state for concurrency
The `queue` concurrency mode currently holds queued triggers in-memory — they are lost on restart.
Persist queued trigger payloads to a `trigger_queue` table in SQLite. On startup, re-hydrate the
in-memory queue from DB before accepting new triggers.
_Revisit: v0.4._

#### [FUTURE] Stdio MCP transport
Support MCP servers running as local subprocesses using the MCP stdio transport (launch a command,
communicate over stdin/stdout). Useful for local development and single-binary deployments
where running a separate MCP HTTP server is inconvenient.
_Revisit: v0.3 or v0.4._

#### [FUTURE] Postgres migration path
Document and provide tooling for migrating the SQLite database to Postgres for operators who
outgrow single-file storage (e.g. high-frequency cron policies generating large run volumes).
The Go data access layer (via sqlc) should require minimal changes — verify at migration time.
_Revisit: when operators request it._

---

### Enterprise Tier

#### [FUTURE] Enterprise tier boundary definition
Define the open-core split: what is AGPL-3.0 and what requires a commercial license.
Candidate enterprise-only features: multi-user RBAC, SSO/OIDC, extended audit retention and export,
approval analytics, cross-policy dependency mapping.
The CLA must be established from the outset — retrofitting after community contributors join is
difficult. Ensure CLA is in place before the first public release.
_Revisit: after OSS launch (ADR-014 stub)._

#### [FUTURE] Extended audit retention and export
Configurable run history retention policy (default: keep all, configurable max age or max rows).
Export run history and reasoning traces to external object storage (S3-compatible).
Useful for compliance and long-horizon analytics.
_Revisit: as part of enterprise tier definition._

---

### Security Hardening

#### [FUTURE] Prompt injection hardening
Wrap MCP tool results in a structured format before inserting them into the agent's context
window. Goal: reduce the attack surface for malicious tool results that attempt to override
the agent's instructions or manipulate subsequent tool calls.
Research current best practices for MCP result sanitization.
_Revisit: when security posture becomes a priority for the target user base, or when a concrete
injection scenario is demonstrated._
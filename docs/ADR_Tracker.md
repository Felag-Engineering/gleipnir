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
| ADR-014 | Poll trigger MCP client architecture               | 🔴 Unresolved | v0.3   | Trigger engine, MCP client, package structure        |
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

---

## ADR-016: Real-time UI transport: SSE over WebSockets

**Status:** Decided
**Date:** 2026-03

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

**Design reference:** `docs/frontend_mockups/` contains four JSX mockups (dashboard, policy editor,
reasoning timeline, MCP registry) that define the visual language and interaction patterns.

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

Both agent implementations (`internal/agent/agent.go` and `internal/agent/claudecode/agent.go`) use the `feedbackCh` channel pattern for blocking on operator response.

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
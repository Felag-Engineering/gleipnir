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
| ADR-006 | React frontend, separate Docker container          | 🟢 Decided    | v0.1   | Frontend, nginx, Docker Compose                      |
| ADR-007 | BoundAgent: sensor / actuator / feedback roles     | 🟢 Decided    | v0.1   | Policy schema, runtime, UI                           |
| ADR-008 | Two approval modes (agent-initiated + policy-gated)| 🟢 Decided    | v0.2   | Approval interceptor, feedback channel               |
| ADR-009 | Feedback channel: policy-first, system fallback    | 🟢 Decided    | v0.2   | Policy schema, notification system                   |
| ADR-010 | Project name: Gleipnir                             | 🟢 Decided    | —      | —                                                    |
| ADR-011 | v1 approval path (UI vs Slack callbacks)           | 🔴 Unresolved | v0.2   | Approval UX, inbound network model                   |
| ADR-012 | Run persistence and recovery behavior              | 🔴 Unresolved | v0.1   | Run executor, storage layer, startup sequence        |
| ADR-013 | System prompt default template                     | 🔴 Unresolved | v0.1   | Agent runtime, policy schema, UI prompt editor       |
| ADR-014 | Poll trigger MCP client architecture               | 🔴 Unresolved | v0.3   | Trigger engine, MCP client, package structure        |
| ADR-015 | Policy concurrency model                           | 🔴 Unresolved | v0.3   | Trigger engine, run executor, policy schema          |
| ADR-016 | Real-time UI transport: SSE over WebSockets        | 🟢 Decided    | v0.1   | Frontend, Go API, nginx, HA scaling path             |
| ADR-017 | Policy-level parameter scoping for MCP tools       | 🟢 Decided    | v0.1   | Policy schema, MCP client, agent runtime, audit log  |
| ADR-018 | Capability snapshot as first run step              | 🟢 Decided    | v0.1   | Run steps schema, agent runtime, reasoning timeline  |

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

**Consequence:** The nginx Docker container config must set `X-Accel-Buffering: no` on SSE
proxy responses to prevent nginx from buffering the event stream.

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

## ADR-006: React frontend, separate Docker container

**Status:** Decided
**Date:** 2026-03

**Decision:** React app served via nginx in a separate container from the Go API. nginx proxies
`/api` requests to the Go container.

**Reasoning:** Separation of concerns, independent iteration on frontend and backend.
Standard pattern for production deployments.

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
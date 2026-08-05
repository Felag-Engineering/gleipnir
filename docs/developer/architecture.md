# Gleipnir Architecture

Gleipnir is a single Go binary that embeds a React frontend, uses SQLite for storage, and orchestrates AI agent runs by talking to external LLM providers and MCP tool servers.

## Diagrams

Each diagram lives in its own file. Start with the system overview and runtime object graph for the big picture, then drill into whichever area you're working on.

| Diagram | What it shows |
|---------|---------------|
| [Architecture at a glance](diagrams/architecture-at-a-glance.md) | Subsystem map, ownership tree, and trigger fan-in — start here |
| [System overview](diagrams/system-overview.md) | Deployment view — what's in the container, what's external |
| [Package dependencies](diagrams/package-dependencies.md) | Internal packages grouped by layer with key import relationships |
| [Runtime object graph](diagrams/runtime-object-graph.md) | What `main.go` creates and how triggers flow through to BoundAgent |
| [Run execution flow](diagrams/run-execution-flow.md) | Full sequence diagram from trigger to run completion |
| [Run state machine](diagrams/run-state-machine.md) | Valid run status transitions (active vs terminal states) |
| [Real-time events](diagrams/realtime-events.md) | SSE broadcaster fan-out from producers to browser clients |
| [Data model](diagrams/data-model.md) | Core database tables and relationships (ER diagram) |
| [Plugin process model](diagrams/plugin-process-model.md) | How plugins are launched, managed, and communicate with the host |
| [Capability enforcement](diagrams/capability-enforcement.md) | How tools are structurally excluded and approval-intercepted |
| [Graceful shutdown](diagrams/graceful-shutdown.md) | Ordered teardown sequence on SIGINT/SIGTERM |
| [Auth and request flow](diagrams/auth-request-flow.md) | HTTP middleware chain from request to handler |

## Key invariants

- **`internal/model` imports nothing internal.** Shared vocabulary; circular imports here would collapse the dependency graph.
- **`internal/mcp` must never import `internal/execution/agent`.** Enforced by the Go compiler.
- **`internal/db` types stay as plain strings.** sqlc generates them from SQLite TEXT columns. Conversion to typed model enums happens in the caller.
- **Approval interception is a hard runtime guarantee.** `BoundAgent.handleToolCall` blocks on the approval channel before forwarding to MCP — not prompt-based, cannot be bypassed by the model.
- **Audit writes are serialized.** `AuditWriter` funnels all `run_steps` inserts through a single goroutine to avoid SQLite write contention.
- **Disallowed tools never exist from the agent's perspective.** They are not registered with the LLM at all (ADR-001).
- **Plugin tools share the MCP tool namespace.** `internal/toolregistry` enforces cross-source `<source>.<tool>` uniqueness at registration time. Collisions are audit-logged and drive the instance to `unhealthy`.
- **Tool-initiated HITL decisions never enter the trace.** `tool_permission_request` / `tool_input_request` are *not* `model.StepType` values; they are `plugin_audit_events.event_type` values written by `internal/plugin/decision`. Making them step types would make them eligible for context replay by construction (ADR-046, ADR-055 §6.6).
- **A tool-initiated request is asked of exactly one channel.** `internal/plugin/hitl` walks the audience in order and the first entry allowed to settle *that kind* of request gets it. Weak-assurance channels are skipped for `permission` asks rather than failing them (ADR-044, ADR-055 §4.1).
- **`WriteAuditStep` is restricted to `feedback_response` only.** Plugins cannot inject arbitrary content into `run_steps` (the LLM's context window). All other plugin events flow into `plugin_audit_events` (ADR-046).

## Plugin subsystem

Gleipnir runs a second extension system parallel to MCP: HashiCorp `go-plugin`-based subprocesses speaking gRPC over Unix domain sockets. Plugins provide tools, triggers, and notification/feedback channels.

- **Process model:** Plugin binaries are dropped into a `/plugins` directory, watched by `fsnotify`, signature-verified (Minisign/TOFU), and launched as subprocesses. See [plugin-process-model.md](diagrams/plugin-process-model.md).
- **Capability enforcement:** Plugin tools and MCP tools share one `<source>.<tool>` namespace via `internal/toolregistry`. ADR-001's hard enforcement applies identically to plugin tools.
- **Host RPCs:** Plugins call back to the host via 8 Tier-1 and 2 Tier-2 gRPC RPCs (`internal/plugin/hostsvc`), authenticated by per-instance identity tokens and gated by generation refcounts.
- **Observability:** Plugin metrics are force-prefixed `gleipnir_plugin_*`; logs ride the `Log` Host RPC for run correlation (ADR-047). Stderr is the fallback for pre-handshake panics only.
- **Trust:** Minisign tamper-evidence with TOFU pubkey pinning (ADR-045). Material manifest changes block hot-reload pending admin approval.
- **Credentials:** Six strategies (none, static_api_key, header_set, basic_auth, oauth2_authcode, oauth2_clientcred). OAuth is host-orchestrated.

The full design specification lives in `docs/developer/plugin-system-spec.md`. ADRs 041-049 record the individual architectural decisions.

## Human-in-the-loop

Three sources can pause a run, unified on the run state machine and — from
ADR-055 — on audiences:

| Source | Owner of the decision to ask | Gate semantics |
|---|---|---|
| Agent-initiated (`gleipnir.ask_operator`, ADR-031) | the agent | voluntary |
| Policy-gated approval (ADR-008) | the policy | pre-execution, mandatory, host-owned |
| Tool-initiated (ADR-055) | the MCP server, mid-call | cooperative — the answer goes back to the server |

Tool-initiated is the newest and has the most moving parts:

- `internal/execution/agent/inputrequired.go` owns the pause lifecycle: MRTR
  `input_required` → durable `tool_input_requests` row + `waiting_for_feedback`
  in one transaction → wait → re-issue the **same** `tools/call` with the answer.
- `internal/execution/agent/replay.go` implements §6.5 answer replay: a server
  whose state expired and then re-asked the *identical* question gets the stored
  answer spent against its fresh `requestState`, once, without a human seeing it.
- `internal/plugin/hitl` picks which audience entry is asked and enforces the
  §4.1 assurance gate host-side.
- `internal/plugin/inapptask` runs the built-in channel's Request on the same
  `mcp_tasks` lifecycle a plugin-routed Request uses, with a NULL `server_id`.
- `internal/plugin/decision` writes the §6.6 decision record to
  `plugin_audit_events` (which carries a nullable `run_id` for exactly this).

Operator-facing: [Human-in-the-loop](../user/human-in-the-loop.md). Server
authors: [Writing a tool that asks a human](tool-initiated-hitl.md).

## Stack summary

| Layer | Technology |
|-------|-----------|
| Backend | Go, chi router, sqlc |
| Frontend | React, TypeScript, Vite, CSS Modules, TanStack Query |
| Storage | SQLite (WAL mode, single file) |
| LLM | Anthropic SDK, Google Gemini SDK, OpenAI API, OpenAI-compatible |
| Tools | MCP over HTTP transport (JSON-RPC) |
| Plugins | HashiCorp go-plugin subprocesses, gRPC/UDS, Minisign signing |
| Real-time | Server-Sent Events |
| Deployment | Docker Compose |
| Embedding | React build served via `go:embed` |

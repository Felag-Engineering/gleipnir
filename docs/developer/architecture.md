# Gleipnir Architecture

Gleipnir is a single Go binary that embeds a React frontend, uses SQLite for storage, and orchestrates AI agent runs by talking to external LLM providers and MCP tool servers.

## Diagrams

Each diagram lives in its own file. Start with the system overview and runtime object graph for the big picture, then drill into whichever area you're working on.

| Diagram | What it shows |
|---------|---------------|
| [System overview](diagrams/system-overview.md) | Deployment view — what's in the container, what's external |
| [Package dependencies](diagrams/package-dependencies.md) | Internal packages grouped by layer with key import relationships |
| [Runtime object graph](diagrams/runtime-object-graph.md) | What `main.go` creates and how triggers flow through to BoundAgent |
| [Run execution flow](diagrams/run-execution-flow.md) | Full sequence diagram from trigger to run completion |
| [Run state machine](diagrams/run-state-machine.md) | Valid run status transitions (active vs terminal states) |
| [Real-time events](diagrams/realtime-events.md) | SSE broadcaster fan-out from producers to browser clients |
| [Data model](diagrams/data-model.md) | Core database tables and relationships (ER diagram) |
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

## Stack summary

| Layer | Technology |
|-------|-----------|
| Backend | Go, chi router, sqlc |
| Frontend | React, TypeScript, Vite, CSS Modules, TanStack Query |
| Storage | SQLite (WAL mode, single file) |
| LLM | Anthropic SDK, Google Gemini SDK, OpenAI API, OpenAI-compatible |
| Tools | MCP over HTTP transport (JSON-RPC) |
| Real-time | Server-Sent Events |
| Deployment | Docker Compose |
| Embedding | React build served via `go:embed` |

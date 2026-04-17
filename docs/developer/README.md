# Developer Documentation

For people writing Gleipnir code. If you're looking for how to *run* Gleipnir, see [`docs/user/`](../user/) instead.

## Contents

### Getting started
- [Building](building.md) — prerequisites, build commands, environment variables.
- [Contributing](contributing.md) — code style, ADR process, PR conventions.
- [Architecture](architecture.md) — diagram index, key invariants, stack overview.
  - [Diagrams](diagrams/) — Mermaid diagrams for system overview, package deps, data flow, state machine, and more.

### Guides
- [Adding a trigger type](adding-a-trigger-type.md) — checklist for implementing a new trigger (enum, handler, validation, DB, frontend).
- [Adding an LLM provider](adding-an-llm-provider.md) — checklist for implementing the `LLMClient` interface and wiring it in.
- [Database workflow](database-workflow.md) — migrations, sqlc queries, and the full add-a-table workflow.
- [Testing patterns](testing-patterns.md) — test helpers in `testutil/`, mock LLM clients, agent integration tests.

### Reference
- [Manual testing](manual-testing.md) — live integration test environment with real MCP servers.
- [Stdio MCP servers](../stdio-mcp-servers.md) — wiring up stdio-only MCP servers via the Supergateway sidecar.
- [Scheduler dispatcher](dispatcher.md) — design reference for the centralized scheduling layer (ADR-036).

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
- [Writing a tool that asks a human](tool-initiated-hitl.md) — for MCP server authors: the MRTR round trip, the idempotency obligation that comes with it, elicitation kinds, and the abuse caps a misbehaving server hits.

### Reference
- [Manual testing](manual-testing.md) — live integration test environment with real MCP servers and the Slack plugin.
- [MCP realignment spec](mcp-realignment-spec.md) — the target architecture for ADR-053…ADR-060 (containerized plugins, the `io.gleipnir/channel` and `io.gleipnir/events` extensions, tool-initiated HITL). The §10–§11 client half and the §6 HITL half have shipped; the container half has not.
- [`io.gleipnir/channel` extension](extension-io-gleipnir-channel.md) — the host↔channel contract: payloads, task lifecycle, assurance declaration, versioning policy, conformance checklist.
- [Manual mode](manual-mode.md) — the operator-owned container posture: what Gleipnir does and does not touch, the label contract, what discovery concludes, a reference compose file, and the four ways to get it silently wrong.
- [Managed MCP endpoints](managed-mcp-endpoints.md) — how a plugin instance becomes an ordinary MCP server entry: the derived trust tier, why a rotation repoints one row instead of adding another, the `io.gleipnir/*` gate, per-server concurrency and queue depth, and why managed rows are not operator-editable.
- [Plugin system spec](plugin-system-spec.md) — full design specification for the go-plugin extension system (process model, services, credentials, trust, observability).
- [Scheduler dispatcher](dispatcher.md) — forward-looking design for a centralized scheduling layer (ADR-036). **Not yet implemented** — the scheduled/poll/cron triggers each still own their own loop today.

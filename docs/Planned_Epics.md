# Planned Epics

## EPIC-001 — Data Model & Storage Layer

**Phase:** v0.1 MVP | **Priority:** P0 | **Status:** In Progress

### Goal

Establish the SQLite schema, data access layer, and core data types that every other component depends on. This is the foundation — nothing else can be built or tested without it.

### Scope

- [x] SQLite database initialization with WAL mode enabled (#1)
- [x] Database migration strategy — version table and manual migration runner (#2)
- [x] Core tables: `policies`, `mcp_servers`, `mcp_tools`, `capability_tags`, `runs`, `run_steps`, `approval_requests` (#3)
- [x] Nullable `thread_id` field on the `runs` table (future Slack threading support) (#3)
- [x] Domain model types and run state enum: `pending`, `running`, `complete`, `failed`, `interrupted` (#4)
- [x] sqlc query definitions for all CRUD operations (#5)
- [x] Serialized audit write queue to handle concurrent run step inserts (#6)
- [ ] Startup scan: on boot, find runs in `running` or `waiting_for_approval` and mark them `interrupted` with the last known step (#7)
- [x] `capability_snapshot` added to the `run_steps` type enum (per ADR-018)

### ADR References

- ADR-003 — SQLite for storage
- ADR-002 — Policy-as-YAML stored in DB
- ADR-007 — Sensor/actuator/feedback roles
- ADR-018 — Capability snapshot as first run step (`capability_snapshot` step type added here)

### Outputs

- Go packages: `internal/db` (sqlc-generated), `internal/model` (domain types)
- Working SQLite database with schema migrations applied on startup

### Risks & Mitigations

- Schema changes after other epics begin will require coordinated migrations — get the core tables right before downstream work starts.
- The audit write queue must be tested under concurrent load to verify serialized writes don't bottleneck run execution.

### Definition of Done

- All core tables created and queryable via sqlc-generated Go code
- Table-driven tests for CRUD operations including concurrent audit writes
- Startup scan correctly marks orphaned runs as interrupted
- Schema passes manual review against the policy YAML structure and run state machine

---

## EPIC-002 — Policy Engine

**Phase:** v0.1 MVP | **Priority:** P0 | **Status:** Not Started

### Goal

Parse, validate, store, and retrieve policy definitions from YAML. The policy is the central configuration primitive — it defines what an agent can do, what triggers it, and what constraints apply.

### Scope

- Policy YAML schema definition (triggers, capabilities with sensor/actuator/feedback tags, system prompt template, concurrency mode, run limits)
- YAML parser and structural validator in Go
- Policy-level fields: `max_tokens_per_run` and `max_tool_calls_per_run` with sensible defaults
- Capability tag validation: warn if a capability tag references a tool not present in the MCP registry (non-blocking at save, blocking at run start — see EPIC-004)
- Store and retrieve policies from SQLite via the data layer (EPIC-001)
- System prompt template rendering with `{{bound_agent.sensors}}`, `{{bound_agent.actuators}}`, and explicit feedback tool injection
- Policy update: re-saving a policy replaces the stored YAML and re-validates
- `params` block on tool entries for policy-level parameter scoping (per ADR-017) — validator warns at save time if a param name doesn't appear in the tool's discovered input schema

### ADR References

- ADR-002 — Policy-as-YAML is the primary configuration primitive
- ADR-007 — Sensor/actuator/feedback roles
- ADR-009 — Feedback channel resolves policy-first, then system fallback
- ADR-017 — Policy-level parameter scoping (`params` is an optional map on any tool entry)

### Outputs

- Go package: `internal/policy` (parser, validator, renderer)
- Validated policy struct usable by the run executor and trigger engine

### Risks & Mitigations

- The YAML schema is the contract between the UI, the trigger engine, and the run executor — changes after downstream epics begin require coordinated updates.
- Template rendering must be injection-safe — user-provided template vars should not allow arbitrary code execution.

### Definition of Done

- YAML round-trips cleanly: parse -> validate -> store -> retrieve -> render produces correct output
- Validation catches: missing required fields, unknown capability tags, invalid trigger configs, non-positive run limits
- System prompt template renders with correct sensor/actuator/feedback tool lists
- Table-driven tests covering valid policies, edge cases, and malformed input

---

## EPIC-003 — MCP Client & Tool Registry

**Phase:** v0.1 MVP | **Priority:** P0 | **Status:** Not Started

### Goal

Build the MCP HTTP client that discovers tools from registered MCP servers and maintains a local registry with Gleipnir-specific capability tags (sensor/actuator/feedback). This is the bridge between Gleipnir's policy model and the external tool ecosystem.

### Scope

- MCP HTTP client in Go: connect to an MCP server URL, call `tools/list` to discover available tools, call `tools/call` to invoke a tool
- MCP server registration: store server URL and connection metadata in SQLite
- Tool discovery: fetch tool list from each registered server, store tool name, description, and input schema
- Capability tag registry: assign sensor, actuator, or feedback role to each discovered tool — managed in Gleipnir's DB, not in the MCP server
- Manual re-discovery: a UI-triggered action that re-fetches tools from a server and surfaces what changed (added/removed/modified)
- Tool invocation: call a specific tool on a specific server with provided input, return the result
- Schema narrowing and call validation for policy-level parameter scoping (per ADR-017) — narrow tool input schemas at run start, validate agent calls before dispatch
- Designed as a standalone package so the poll trigger engine (EPIC-008) can reuse the client without importing the agent runtime

### ADR References

- ADR-004 — MCP-native, HTTP transport first
- ADR-001 — Hard capability enforcement at runtime
- ADR-017 — Schema narrowing applied at run start; call validation applied before dispatch; rejection errors written to audit log

### Outputs

- Go package: `internal/mcp` (client, registry, capability tags)
- Registered MCP servers and tagged tools queryable from the DB

### Risks & Mitigations

- MCP servers are fully trusted — a compromised server can expose arbitrary tools. This is a documented deployment assumption, not a code-level mitigation.
- MCP server unavailability during discovery or invocation needs clear error propagation, not silent failure.
- If the client is tightly coupled to the agent runtime, extracting it for the poll trigger later will require refactoring — keep the package boundary clean from the start.

### Definition of Done

- Client connects to a live MCP server (or test stub), discovers tools, and stores them with capability tags
- Tool invocation sends correct input and returns the result or a clear error
- Re-discovery detects added, removed, and changed tools
- Package has no import dependencies on the agent runtime
- Integration test against a mock MCP server covering: discovery, invocation, server-down error path

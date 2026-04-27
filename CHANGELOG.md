# Changelog

All notable changes to Gleipnir will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

### Changed

### Fixed

### Security

## [1.0.0] - 2026-04-27

Initial public release.

### Added

- **Hard capability enforcement** (ADR-001). Tools not granted by a policy are never registered with the agent. There is no prompt-based restriction to bypass.
- **Policy-gated approval** (ADR-008, ADR-029). Tools marked `approval: required` are intercepted by the runtime before execution, with timeout enforcement and an explicit state machine.
- **Native feedback channel** (ADR-031). Agents can request operator input via `gleipnir.ask_operator`; the runtime manages the `waiting_for_feedback` state and timeout.
- **Five trigger types**: `webhook` (HMAC or bearer auth), `manual`, `scheduled` (one-shot ISO-8601 timestamps), `poll` (recurring MCP probe with JSONPath checks), and `cron` (5-field POSIX expression).
- **Multi-provider LLM support** (ADR-026). Anthropic, Google, OpenAI, and any OpenAI-compatible backend behind a common `internal/llm` interface. Provider API keys are configured through the admin UI and stored encrypted.
- **MCP over HTTP transport** (ADR-004). Capability tags (`tool` / `feedback`) are stored in Gleipnir's DB, not the MCP server. Authenticated MCP servers (ADR-039) are supported via per-server encrypted auth headers.
- **Arcade gateway pre-authorization** (ADR-040). One-click toolkit OAuth for Gmail, Google Calendar, Slack, GitHub, and other Arcade-hosted MCP toolkits.
- **Encrypted secrets at rest.** Provider API keys, OpenAI-compatible backend keys, webhook secrets, and MCP auth headers are encrypted with AES-256-GCM under `GLEIPNIR_ENCRYPTION_KEY`.
- **Atomic encryption-key rotation.** `gleipnirctl rotate-key` re-encrypts every at-rest secret in a single transaction, with `--dry-run` validation. See `cmd/gleipnirctl/README.md`.
- **Atomic run-state transitions** (ADR-038). Every status change runs in a transaction with a version-column CAS guard.
- **Capability snapshot as first run step** (ADR-018). Every run records the exact tools registered at run start.
- **Reasoning trace.** Every step the agent takes — thoughts, tool calls, results, approvals, feedback — is recorded for after-the-fact review.
- **Role-based access control.** Four roles (`admin`, `operator`, `approver`, `auditor`) enforced by middleware.
- **Server-Sent Events** (ADR-016) push run status changes, new steps, and approval events to the UI in real time.
- **Embedded React frontend.** The full UI is compiled into the Go binary via `go:embed` and served directly — no separate frontend container.
- **Three end-to-end playbooks.** Meal planning (Google Calendar + Mealie), Todoist research (DuckDuckGo + Todoist), and homelab DevOps (Docker + Proxmox + Technitium + Caddy).

### Security

- All provider API keys, webhook secrets, and MCP auth headers are encrypted with AES-256-GCM at rest.
- Webhook secrets live in a dedicated encrypted column outside the policy YAML blob (ADR-034); rotate/reveal is restricted to `admin` and `operator` roles.
- MCP auth header values are write-only over the API; `GET` returns header *keys* only (ADR-039).
- Passwords are bcrypt-hashed at cost 12. Session cookies are `HttpOnly`, `SameSite=Lax`, and `Secure` over HTTPS.
- See [SECURITY.md](SECURITY.md) for the full threat model and security controls.

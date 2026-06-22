# Changelog

All notable changes to Gleipnir will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

### Changed

### Fixed

### Security

## [1.1.0] - 2026-06-22

The plugin system. Gleipnir can now be extended with signed, out-of-process plugins that add tools, channels, and event triggers alongside MCP. This release also hardens the LLM provider layer, the shutdown path, and the authentication surface.

### Added

- **Plugin system (ADR-041).** First-party extension model running HashiCorp go-plugin subprocesses over gRPC/UDS, parallel to MCP. One plugin binary can expose any of three capability surfaces:
  - **Tools** — agent-callable tools that share the single `<source>.<tool>` namespace with MCP. Hard capability enforcement (ADR-001), parameter scoping (ADR-017), and approval gating (ADR-008) apply identically.
  - **Channels (ADR-044)** — `Notify` (fire-and-forget fan-out) and `Request` (exactly-one routing with async callback) for human-in-the-loop. Routing is configured through named, admin-managed **audiences** referenced by policy. In-app feedback (ADR-031) is now itself a channel; `gleipnir.in-app` is the always-available fallback.
  - **Triggers (ADR-048).** A new internal `subscribed` trigger type binds a policy to a `(source, event_kind)` pair declared by a plugin. Bindings use typed form fields (regex / contains / equals) from the manifest's `binding_schema` — no JSONPath.
- **Plugin signing & TOFU trust (ADR-045).** Minisign (Ed25519) tamper-evidence with trust-on-first-use pubkey pinning. Updates must verify against the pinned key; rotation requires admin approval ("Accept new key"). Material manifest changes block hot-reload pending admin re-approval. `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true` is the development escape hatch (loud, global, audited).
- **Host-side OAuth2 + credential management.** Six auth strategies (`none`, `static_api_key`, `header_set`, `basic_auth`, `oauth2_authcode`, `oauth2_clientcred`); the host owns the OAuth dance and a refresh scanner. Credentials are encrypted at rest and pulled by plugins on demand (never cached plugin-side).
- **Admin plugin surfaces.** `/admin/plugins` (install → pending-review → approve, per-instance lifecycle, health, RSS), `/admin/audiences` (channel routing), and a plugin trigger picker in the policy editor.
- **Plugin observability (ADR-047).** Plugin metrics are force-prefixed `gleipnir_plugin_` with auto-injected `plugin`/`instance` labels and a per-label cardinality cap; plugin logs ride a host RPC with run/policy correlation.
- **Plugin SDK & CLI.** The `plugin-sdk/` module and `gleipnir-plugin` CLI (`new`, `gen-manifest`, `validate`, `keygen`, `sign`, `package`, `run`) for building, signing, and packaging plugins. Two first-party reference plugins ship in-repo: **ntfy** (minimal, channel-only) and **Slack** (kitchen-sink — all three surfaces + OAuth). See the new [Plugin Author Guide](docs/developer/plugin-author-guide.md).
- **Persistent event dedup.** Plugin substrate events are deduplicated through a SQLite-backed rolling-window store with a background sweeper, so at-least-once event delivery does not double-fire policies (`GLEIPNIR_PLUGIN_DEDUP_SWEEP_INTERVAL`, fixed 1-hour window).
- **Graceful LLM retry.** Transient provider failures — connection errors, HTTP 408/429 rate limits, and transient 5xx — are now retried with full-jitter exponential backoff that honors a provider `Retry-After` hint (`GLEIPNIR_LLM_RETRY_*`). Uniform across Anthropic, OpenAI, Google, and OpenAI-compatible backends.
- **OpenAI-compatible reasoning.** `reasoning_content` from OpenAI-compatible backends is now surfaced as `thinking` steps in the reasoning trace, with hardened streaming retry and gpt-5 support.
- **`gleipnirctl create-user`.** New admin CLI subcommand to provision users directly against the database.
- **Operator plugin runbook.** Operator-facing plugin documentation, environment-variable reference, and a README plugins section.

### Changed

- **`GLEIPNIR_PLUGINS_ENABLED` has been removed.** The plugin system is now an unconditional part of the host. The flag existed only as a temporary rollout mechanism and never appeared in a tagged release; operators tracking `main` who set it should drop it — it is now ignored (spec §15.2).
- **Provider layer refactor (ADR-026).** Each LLM provider now sits behind a shared `ProviderWire`/`ProviderAdapter` seam that owns the common request/metrics choreography, with a cross-wire contract test suite. The OpenAI-compatible loader drops the brittle model-name reasoning heuristic in favor of an explicit flag.
- **Opus 4.8** added to the model catalog.

### Fixed

- **Graceful shutdown drain.** The scheduler, cron runner, poller, and the approval/feedback/plugin-request timeout scanners are now drained on shutdown within `GLEIPNIR_DRAIN_TIMEOUT`, and a `Poller.Stop()` deadlock was fixed.
- **Run-state, trigger, and provider correctness.** A batch of fixes to run-state transitions, trigger dispatch, and provider handling; poll policies now always show their next fire time while active.
- **Plugin lifecycle hardening.** Generation-drain context cancellation, feedback-response rollback on CAS conflict, cached channel-dispatch connection cleanup on shutdown, OAuth scanner shutdown join, plugin migration baseline execution, and the `plugin_pending_requests` reclaim scanner startup.
- **ntfy reference plugin** credential setup (corrected auth-strategy literal) and README CLI examples.

### Security

- **TOFU pubkey-pin bypass fixed.** A same-version binary backfill path could skip the trust-on-first-use pinned-key check; it now verifies against the pinned key on every load (ADR-045).
- **Authenticated SSE + login rate-limiting.** The `/api/v1/events` SSE stream now requires authentication, and login attempts are rate-limited per IP.
- **Plugin trust boundary.** Plugins run as out-of-process subprocesses gated by per-instance identity tokens and a generation-refcount drain; plugin-emitted audit callbacks are authenticated by request ownership and restricted to the `feedback_response` step type (ADR-046). Plugin credentials are encrypted at rest under `GLEIPNIR_ENCRYPTION_KEY`. See [SECURITY.md](SECURITY.md) for the plugin trust model.

## [1.0.0] - 2026-04-29

Initial public release.

### Added

- **Hard capability enforcement.** Tools not granted by a policy are never registered with the agent. There is no prompt-based restriction to bypass.
- **Policy-gated approval.** Tools marked `approval: required` are intercepted by the runtime before execution, with timeout enforcement and an explicit state machine.
- **Native feedback channel.** Agents can request operator input via `gleipnir.ask_operator`; the runtime manages the `waiting_for_feedback` state and timeout.
- **Five trigger types**: `webhook` (HMAC or bearer auth), `manual`, `scheduled` (one-shot ISO-8601 timestamps), `poll` (recurring MCP probe with JSONPath checks), and `cron` (5-field POSIX expression).
- **Multi-provider LLM support.** Anthropic, Google, OpenAI, and any OpenAI-compatible backend behind a common `internal/llm` interface. Provider API keys are configured through the admin UI and stored encrypted.
- **MCP over HTTP transport.** Capability tags (`tool` / `feedback`) are stored in Gleipnir's DB, not the MCP server. Authenticated MCP servers are supported via per-server encrypted auth headers.
- **Arcade gateway pre-authorization.** One-click toolkit OAuth for Gmail, Google Calendar, Slack, GitHub, and other Arcade-hosted MCP toolkits.
- **Encrypted secrets at rest.** Provider API keys, OpenAI-compatible backend keys, webhook secrets, and MCP auth headers are encrypted with AES-256-GCM under `GLEIPNIR_ENCRYPTION_KEY`.
- **Atomic encryption-key rotation.** `gleipnirctl rotate-key` re-encrypts every at-rest secret in a single transaction, with `--dry-run` validation. See `cmd/gleipnirctl/README.md`.
- **Atomic run-state transitions.** Every status change runs in a transaction with a version-column CAS guard.
- **Capability snapshot as first run step.** Every run records the exact tools registered at run start.
- **Reasoning trace.** Every step the agent takes — thoughts, tool calls, results, approvals, feedback — is recorded for after-the-fact review.
- **Role-based access control.** Four roles (`admin`, `operator`, `approver`, `auditor`) enforced by middleware.
- **Server-Sent Events** push run status changes, new steps, and approval events to the UI in real time.
- **Embedded React frontend.** The full UI is compiled into the Go binary via `go:embed` and served directly — no separate frontend container.
- **Three end-to-end playbooks.** Meal planning (Google Calendar + Mealie), Todoist research (SearXNG + Todoist), and homelab DevOps (Docker + Proxmox + Technitium + Caddy).

### Security

- All provider API keys, webhook secrets, and MCP auth headers are encrypted with AES-256-GCM at rest.
- Webhook secrets live in a dedicated encrypted column outside the policy YAML blob; rotate/reveal is restricted to `admin` and `operator` roles.
- MCP auth header values are write-only over the API; `GET` returns header *keys* only.
- Passwords are bcrypt-hashed at cost 12. Session cookies are `HttpOnly`, `SameSite=Lax`, and `Secure` over HTTPS.
- See [SECURITY.md](SECURITY.md) for the full threat model and security controls.

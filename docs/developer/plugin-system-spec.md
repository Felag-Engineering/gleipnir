# Plugin System — Design Specification

**Status:** Implemented (v1) — design tree closed 2026-05-02; shipped on the `plugin-architecture` branch (ADRs 041–050). This document is retained as the design rationale; the ADR_Tracker is the source of truth for what is decided and implemented. Items still explicitly deferred (event dedup #215, typed plugin-emitted metric counters, per-policy/per-user credential scoping) are called out inline and in §15.
**Audience:** Engineers, reviewers, and agents working on or extending the plugin system.
**Companion docs:** `architecture.md` (system overview), `ADR_Tracker.md` (existing ADRs the plugin system extends or supersedes).

---

## 1. Summary

Gleipnir today exposes external functionality through MCP servers (HTTP transport, capability tags maintained host-side). MCP is great for power users with custom tooling but is not approachable to non-engineer extension authors and does not give Gleipnir first-party control over the user-facing surface (auth flows, structured config, signed releases, observability).

This document specifies a **second extension system** running parallel to MCP: a **HashiCorp `go-plugin`-based subprocess plugin model** with strong host-side affordances for non-engineer authors and operators. MCP remains the power-user escape hatch and is unchanged.

The plugin system in v1 covers three capability surfaces:
- **Tools** — agent-callable functions, sharing one namespace with MCP tools
- **Triggers** — fire runs in response to external events (e.g. a Slack message)
- **Channels** — unified surface for both fire-and-forget notifications and request/response feedback (replaces ADR-031's bespoke in-app feedback machinery as the dispatcher abstraction)

A single plugin binary may declare any subset of the three.

## 2. Goals and non-goals

### Goals
1. **Non-engineer extensibility.** Operators install signed plugins; plugin authors write Go against an SDK with codegen, scaffolding, and a local dev REPL.
2. **First-party ecosystem control.** Gleipnir owns OAuth flows, credential storage, audit, observability, and admin UI. Plugins focus on substrate logic.
3. **Hard capability enforcement preserved.** ADR-001's principle (disallowed capabilities are never registered with the agent) carries forward unchanged. Plugin tools obey the same per-policy grants, parameter scoping, approval gating, and capability snapshot rules.
4. **Operator audit.** Every install, key rotation, manifest change, credential lifecycle event, and authorization is recorded.
5. **Tamper-evidence in v1.** Signed binaries with TOFU pubkey trust. Identity attestation (Sigstore-keyless) is deferred until a plugin storefront exists.
6. **One-binary, multi-capability.** A plugin like Slack ships triggers + tools + channels behind one OAuth dance and one install action.

### Non-goals (v1)
- Lifecycle hook plugins or LLM provider plugins (deferred — see §15).
- Cross-plugin RPCs, plugin enumeration of other plugins, or direct DB access.
- Hard sandboxing (cgroup/seccomp/namespaces per plugin process). Plugins are admin-installed and trusted on install.
- Multi-tenant scoping. Gleipnir is single-tenant; per-team or per-user credential routing is a v2 expansion that this design is forward-compatible with (§4.4).
- Replacement of MCP. Both stacks coexist permanently.

## 3. Architecture

### 3.1 Process model

```
┌──────────────────────────────────────────┐
│ Gleipnir API container                   │
│                                          │
│  ┌─────────────────────────────────────┐ │
│  │ Gleipnir host process (Go)          │ │
│  │  - run lifecycle, audit, scheduling │ │
│  │  - admin UI, REST API, SSE          │ │
│  │  - plugin manager, audit, dispatch  │ │
│  └────────┬───────────────────┬────────┘ │
│           │ gRPC/UDS          │ HTTP     │
│  ┌────────▼─────────┐  ┌──────▼───────┐  │
│  │ plugin (subproc) │  │ MCP server   │  │
│  │ slack-prod       │  │ (external)   │  │
│  └──────────────────┘  └──────────────┘  │
│  ┌──────────────────┐                    │
│  │ plugin (subproc) │                    │
│  │ ntfy             │                    │
│  └──────────────────┘                    │
└──────────────────────────────────────────┘
```

Plugin binaries are dropped into a mounted `/plugins` directory. The host watches via `fsnotify`, verifies signatures, and launches each plugin as a subprocess speaking gRPC over a Unix domain socket via `go-plugin`.

### 3.2 Plugin vs instance

- **Plugin** = a binary + manifest pair shipped by an author. Identified by name + version. Has a single signing key (TOFU-pinned at install).
- **Instance** = a configured deployment of a plugin. Carries credentials and per-deployment config. One plugin can be instantiated multiple times (e.g. `slack-prod` and `slack-personal`).
- **Generation** = a specific subprocess incarnation of an instance. Replaced on hot-reload; old generations may continue serving in-flight calls until they drain.

### 3.3 Capabilities and the agent

Hard capability enforcement (ADR-001) is preserved:
- Plugin tools and MCP tools live in **one namespace**: `<source>.<tool>`, where `<source>` is either an MCP server name or a plugin instance name. Uniqueness is enforced across both registries at registration time.
- Agents only see the tools their policy grants. Tools not granted are never registered with the LLM.
- Capability snapshot (ADR-018) records all granted tools at run start, regardless of source.
- Per-policy parameter scoping (ADR-017) and approval gating (ADR-008) apply to plugin tools identically. Implementation invariant: the approval interceptor in `handleToolCall` runs BEFORE source-specific dispatch (MCP transport or plugin generation guard); there is exactly one approval chokepoint.
- The agent never sees `gleipnir.ask_operator` directly. Internally it sees a synthetic `request_feedback` tool; the runtime resolves the audience and routes to the appropriate Channel implementation. From the agent's perspective, who gets paged is not its concern.

## 4. Plugin model

### 4.1 Services (gRPC)

A plugin binary may implement any combination of:
- `ToolService` — `ListTools`, `Call`
- `ChannelService` — `Notify`, `Request`
- `TriggerService` — `Start` (long-lived stream), event emission via Host RPC

Plus the always-present:
- `Handshake/v1` — protocol negotiation (never bumps; treated as immortal)
- `grpc.health.v1.Health` — standard liveness probe

The manifest declares which services the binary implements; the handshake at startup confirms the binary actually serves what the manifest claims. Handshake-declared capabilities **must be a subset** of manifest-declared. Mismatch → kill plugin, audit, mark instance unhealthy.

### 4.2 Channel model

`ChannelService` unifies notifications and request/response feedback under one contract:

- **`Notify`** — fire-and-forget, fan-out OK. Per-call deadline 10s. Failures are audited and metric-counted but DO NOT fail the run.
- **`Request`** — request/response, exactly one channel per request. **Async via callback**: plugin synchronously acks the request (5s pre-ack deadline), then later calls the host's `WriteAuditStep` Host RPC with a `feedback_response` step when the human replies. Rides Gleipnir's existing scan-and-resolve timeout machinery (`internal/timeout/`).

  **Lifecycle across generations.** `request_id` is **instance-scoped**, not generation-scoped — required because hot-reload can occur while a Request is awaiting human response. A new generation may service a callback for a request issued by a previous generation if the substrate connection state is recoverable (substrate-dependent). When an old generation is force-killed at the 60s drain grace (§13.3) without ever delivering, outstanding requests resolve via the normal feedback timeout path; runs in `waiting_for_feedback` are NOT eagerly failed at force-kill. Substrate side may still surface to a human and be answered against the new generation.

  **Late callbacks.** `WriteAuditStep` for a `request_id` whose feedback request has already been resolved (response received via another path, or timed out) is rejected and recorded as a `feedback_response_late` event in `plugin_audit_events`. Run state is not mutated.

  **Pre-ack failure** still fails the run fast (`feedback_dispatch_error`).

Routing decisions (which channels notify, which one handles a request) are **host-side** based on the policy's audience configuration. See §6 for audience semantics.

### 4.3 Trigger model

Triggers do not directly fire runs. A plugin's `TriggerService.Start` is a long-lived stream where the plugin emits events via the Host RPC `EmitEvent(kind, payload)`. The host:
1. Receives events.
2. Scans policies that have a `subscribed` trigger binding referencing this `(plugin_instance, event_kind)`.
3. For each match, evaluates the policy's structured binding (regex/contains/etc.) against the payload.
4. Fires matching policies through the existing `RunLauncher` (so per-policy concurrency caps and rate limits already apply).

Coarse subscription scope (e.g. "watch only `#incidents` and `#ops`") is configured once on the plugin instance to limit chattiness. Per-binding refinement happens in the policy editor.

A plugin-instance-level rate limit drops excess events (incrementing a metric) to protect the host from runaway substrates.

**Event delivery semantics.** `EmitEvent` is at-least-once. Plugins MUST include a stable `event_id` in the event envelope; the host dedupes against a 1-hour rolling window per `(plugin_instance, event_kind, event_id)`. Plugins that cannot synthesize a stable id from the substrate (e.g. a webhook substrate that delivers without a sequence number) should hash the canonical payload as a fallback — duplicate detection outside the stated window is best-effort. Ordering is per-stream (one Trigger `Start` stream preserves order); cross-stream ordering is not guaranteed.

### 4.4 Instance scoping

An instance is a **credential/config envelope** — not necessarily 1:1 with end-user identity. Each service in the manifest declares its credential mode:

- **`instance_credentials`** (v1 default) — one credential set serves all calls. For multiple credentials, use multiple instances.
- **`user_credentials`** (v2) — host stores per-user credentials, passes a `user_id` on every call. OAuth flow per user inside one instance.

**v1 ships only `instance_credentials`.** For forward compatibility, all Host RPCs and plugin-side service RPCs reserve a `user_id` field in the proto. It is always empty in v1; landing the plumbing now (one PR, all empty) is much cheaper than retrofitting call sites later. Adding it later is a non-breaking change per §10.

The admin UI list view follows `plugin → instances` today; v2 adds a third tier of `instance → authorized users` without UI rework.

## 5. Distribution and trust

### 5.1 Distribution

- **v1 = filesystem dropin only.** No curated registry, no upload-via-UI.
- Plugin tarballs (built by `gleipnir-plugin package`) are dropped into the mounted `/plugins` directory. The bundle contains the binary, `manifest.yaml`, `<name>.minisig`, `signing.pub`, and optionally `sbom.cyclonedx.json`.
- `fsnotify` triggers the install/update path. New manifests land in **"Pending review"** state — admin must click through to activate, surfacing all declared capabilities (see §11.4).
- **Hot-reload** is supported. In-flight runs hold a reference to a specific generation; new tool calls against a replaced generation fail with a runtime error (preserves the ADR-018 capability snapshot guarantee).

### 5.2 Signing

- **Scheme: Minisign (Ed25519).** ~200 LOC of verification, no infrastructure dependencies, self-contained `(binary, signature, pubkey)` triplet, works airgapped. Sigstore is deferred to a future storefront-era v2.
- **Signed payload:** `sha256(binary) || sha256(manifest)` as one unit. Hash raw manifest bytes (no canonicalizer in v1; the SDK produces tamper-resistant signed bundles).
- **What v1 buys:** tamper-evidence. Not author identity attestation. This is honest framing.

### 5.3 Trust on first use (TOFU)

- On first install of a plugin, capture the embedded `signing.pub` into `plugin_instances.trusted_pubkey`.
- Updates must be signed by the captured key. Mismatch → block + admin "Accept new key" UI.
- Manual admin approval for key rotation (rotation certificates deferred to v2).
- Advanced toggle at install: paste a pubkey out-of-band to pin (skips TOFU first-leap).
- Acknowledged limit: TOFU's first install is a leap of faith. The `plugins:install` admin permission gate is the bridge until storefront-era identity attestation.

### 5.4 Validation timing

| Trigger | Action |
|---------|--------|
| Install | Verify signature, capture pubkey, snapshot manifest into DB |
| Plugin process start | Verify signature against snapshotted manifest |
| Hot-reload (`fsnotify`) | Verify signature; if manifest has **material changes**, block reload pending admin re-approval |
| Per-RPC | None — verification is process-boundary, not call-boundary |
| Background scan | None — next process start covers it |

**Material manifest changes** (block reload until admin re-approves): pubkey claim, declared services, Tier-2 capability declarations, OAuth scopes/strategy, declared tool list, instance `config_schema` shape, `event_kinds[].binding_schema` shape. **Cosmetic changes** (description, version string, author email, default values, JSON Schema descriptions) flow silently with audit log.

**Config schema migrations are v2 scope.** A material `config_schema` change blocks hot-reload until admin reviews each existing instance — instances with newly-required fields enter `pending_config_migration` health state, and admin must edit each instance config before the new generation activates. No automated migration tooling in v1.

### 5.5 Failure modes

| Condition | Behavior |
|-----------|----------|
| Invalid signature | Hard block. No override. |
| TOFU violation | Block + "Accept new key" UI. |
| Material manifest change on hot-reload | Block reload, pending admin approval. |
| Verification system error (missing `.minisig`, I/O) | Fail closed, surface detailed error. |
| Unsigned plugin | Block by default. Override: global env var `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true` (not per-plugin). When set: red banner across admin UI, high-severity audit events on every load, `signature_verification: disabled` in `/api/v1/health`. Even in permissive mode, signed plugins are still fully verified. |
| Hot-reload failure on running plugin | Old generation drains in-flight requests; new generation never starts. Admin sees "serving in-flight, no new requests accepted" with View error / Revert / Remove pending update actions. |

### 5.6 Per-instance health states

`healthy`, `signature_invalid`, `pending_key_approval`, `pending_manifest_approval`, `pending_config_migration`, `verification_error`, `unsigned_permissive`. Surfaced as colored chips in admin UI; click for detail and admin actions.

## 6. Audiences and channel routing

Audiences are **first-class shared resources**, edited at `/admin/audiences`, referenced by name from policy YAML.

### 6.1 Audience structure

An audience = name + ordered list of channel entries. Each entry:
```
- plugin_instance: slack-prod
  notify: true
  request: true
  config:
    channel: "#incidents"
    mention: "@oncall"
```

The per-entry `config` block is validated against the channel plugin's manifest `config_schema` (JSON Schema, reflection-derived from Go struct types — see §13.4).

### 6.2 Routing semantics

- **`Notify`** fans out **in parallel** to ALL entries with `notify: true` whose plugin implements Notify. Best-effort; failures audited and metric-counted but DO NOT fail the run. Worst-case 10s total regardless of fan-out width (per-call deadline).
- **`Request`** is routed to the **first ordered entry** with `request: true` whose plugin implements Request. No inter-channel fallback. Pre-ack failure → `feedback_dispatch_error` step + run fails fast. Post-ack timeout → existing `internal/timeout/scanner.go` machinery.
- **`gleipnir.in-app` is auto-appended** as the lowest-priority entry of every audience by default. Advanced toggle to disable per audience. Validation: if disabled AND audience has zero Request-capable entries, save fails. Guarantees first-class feedback (ADR-031) always has a landing surface.

### 6.3 Partial channel implementations

Plugins declare in their manifest which of `Notify`/`Request` their `ChannelService` implements. The audience editor disables per-entry toggles accordingly (e.g. ntfy is Notify-only — "Send Request?" toggle disabled with explanatory tooltip).

### 6.4 Per-event-type routing overrides

Deferred. v1 audiences are flat. Per-event-type routing (e.g. `run_failed → pagerduty`, `feedback_request → slack-ops`) requires a future policy schema bump. Add when a real user demands it.

## 7. Subscribed triggers

### 7.1 Policy-side binding

A new internal `trigger_type: subscribed` is added to the policy YAML schema. **The trigger picker UI is flat at the conceptual level** — operators see built-ins (`webhook`, `manual`, `scheduled`, `poll`, `cron`) and every plugin event_kind as peer options. Multiple instances of the same plugin contribute disambiguated entries (`Slack (slack-prod): Channel message` vs `Slack (slack-personal): Channel message`). The internal `subscribed` type is never rendered as a label.

At scale (several plugins × several event_kinds), the picker groups visibly by source — built-ins are a single top-of-list group; each plugin instance forms its own group; a search box filters across all entries. The grouping is purely presentational; the underlying selection is still a single `(source, event_kind)` pair.

### 7.2 Binding configuration

Per-policy trigger config is a structured form rendered from the manifest's `event_kinds[].binding_schema` (JSON Schema, reflection-derived from Go struct types). Plugin authors express filters via richer fields (regex, contains, equals, etc.) — **JSONPath is not used for plugin trigger filtering.** (JSONPath remains in the built-in `poll` trigger, which evaluates MCP tool output.)

```yaml
trigger:
  type: subscribed              # internal, never rendered in UI
  source: slack-prod            # plugin instance
  event_kind: channel_message
  binding:
    channel: "#incidents"
    mention_only: true
```

### 7.3 Runtime evaluation

Host-side, field-typed operators against the event payload. No per-match round-trip RPC to the plugin. Plugins do not implement `MatchBinding`; richer filters expand the schema additively when needed.

### 7.4 v1 limit

One trigger binding per policy. Multiple triggers per policy is deferred. The structured-form approach generalizes cleanly when needed.

### 7.5 Test against sample

Plugin manifests provide `examples` in each `event_kind` declaration. Policy editor offers a **Test binding against sample** button per saved binding. No paste-your-own-JSON in v1.

**Declaring examples (SDK):** The canonical typed path is `Manifest.AddEventKindWithExamples`, which accepts `...manifest.Example{Name, Payload}` structs and round-trips the payload through `yaml.Marshal`. The raw-node path `AddEventKind(..., examples ...*yaml.Node)` remains supported for callers that construct nodes directly.

**Server-side evaluation:** Clicking "Test against sample" in the policy editor sends the current binding + example payloads to `POST /api/v1/admin/plugin-instances/{iid}/event-kinds/{kind}/test-binding`. The server evaluates using `internal/plugin/binding.Compile` + `Evaluate` — the same code path as the runtime dispatcher — so Go RE2 semantics are used, not browser JavaScript regexes.

**Client-side design:** The client sends payloads back in the request body (stateless endpoint). This avoids hot-reload drift between the list call that returned examples and the test call, and removes any server-side caching requirement.

**Known sharp edge (v1):** Only manifest-declared examples are tested. Operators bound to events whose canonical payload differs from the manifest examples must wait for v2 paste-your-own-JSON. **v1 workaround:** capture a real event with `gleipnir-plugin run --capture` against a dev instance, then iterate locally with `--replay <captured_event.json>`.

## 8. Host API (plugin → host RPCs)

### 8.1 Tier 1 (always-on)

| RPC | Purpose |
|-----|---------|
| `GetInstanceConfig` | Returns the instance's structured config (no audit). |
| `GetCredentials(user_id)` | Returns decrypted credentials. `user_id` reserved for v2; empty in v1. Mutation events go to `plugin_audit_events`; reads to slog only. |
| `GetRunContext` | Returns `{run_id, policy_id, started_at, step_index}` — IDs only, no payload. |
| `WriteAuditStep` | Writes a step into `run_steps` for the active run context. **Allowed step types: `feedback_response` only** (carries `request_id`; see §8.4). `tool_call` and `tool_result` are host-authored — a plugin's tool call returns the result via the service RPC and the host writes the step. Other step-type writes are rejected and logged as `unauthorized_step_type`. |
| `EmitMetric(name, value, labels)` | Emits a Prometheus metric. Host force-prefixes `gleipnir_plugin_` and auto-injects `plugin` and `instance` labels. Cardinality cap per-label (~100 distinct values) — emits beyond the cap are **rejected with an error returned to the plugin** (loud failure surface so plugin authors catch unbounded labels in dev rather than discover them via silent drops in production). |
| `EmitEvent(kind, payload)` | Trigger plugins emit events to the host. |
| `Log(level, msg, attrs)` | Plugin → host structured log. Host resolves `run_id`/`policy_id`/`step_index` from the propagated `call_id` (§8.5) and routes through slog with logctx correlation. |
| `SetHealthState(state, detail)` | Plugin self-reports health. Plugin can only mark itself worse than host-detected state; worst across plugin-self and host-detected wins. |

**Why `Log` over stdout:** gRPC calls are concurrent. Multiple runs may invoke one plugin at once; stdout cannot be reliably attributed to a specific `run_id`. The `Log` RPC carries the `call_id` from §8.5 so the host can correlate every line. Stdout/stderr are still captured per-line as fallback for panics outside any active call.

### 8.2 Tier 2 (manifest-declared, admin-approved at install)

- `run_history_read` — read past runs/steps for the calling instance's policies
- `user_directory_read` — read user/role information

Tier-2 declarations appear in the install consent screen.

### 8.3 Tier 3 (out for v1)

Cross-plugin reads, enumerate-installed, mutation, direct DB access. All explicitly rejected for v1.

### 8.4 Authorization model for Host RPC calls

Each plugin process has a unique gRPC connection identity assigned at subprocess launch (rotates per generation). The host verifies the calling identity on every Host RPC. Identity rotates per generation but **authorization scope is the instance**: for `WriteAuditStep` of a `feedback_response`, the response's `request_id` must have been routed to the calling instance — even if the routing happened under a previous generation that has since been replaced. Mismatch → `unauthorized_request_id` audit event at high severity.

### 8.5 Call context propagation

Every host→plugin service RPC injects a host-generated `call_id` into gRPC metadata (`gleipnir-call-id`). Plugins MUST propagate this id back on every Host RPC made from within that call's handler scope; the host uses it to correlate `Log`, `EmitMetric`, and `WriteAuditStep` writes to the originating run, policy, and step.

- The SDK's request-context helpers thread the call_id automatically through `serve.WithCallContext(ctx)`. Plugin authors using the standard handler signature get this for free.
- Background goroutines spawned by the plugin must explicitly capture and pass the call_id, or use `serve.DetachContext(ctx)` to mark the work as call-detached (call_id elided; logs flow through `plugin`/`instance` labels only).
- Host RPCs lacking a valid call_id (e.g. `EmitEvent` from a long-lived Trigger stream, or panic capture from outside any active call) are still accepted and logged with `plugin`/`instance` labels only; **`WriteAuditStep` from a call-detached context is rejected** and recorded as an `unauthorized_call_context` audit event.
- Channel Request post-ack callbacks are exempt from call_id correlation: the host issues a separate `request_id` token at routing time, and the plugin's eventual `WriteAuditStep` for `feedback_response` carries that `request_id` instead (see §4.2 and §8.4).
- The `call_id` doubles as the cancellation handle (§13.8).

## 9. Authentication and credentials

### 9.1 Strategies in v1

- `none` — no credentials required; `credentials_encrypted` may be absent.
- `static_api_key` — one secret header. Storage shape: `{header_name, scheme?, api_key}` inside `credentials_encrypted`. See `internal/plugin/oauth.StaticAPIKeyCreds`.
- `header_set` — one or more named HTTP headers (generalises ADR-039). Storage shape: `{headers: [{name, value}]}`. Reserved headers (`Mcp-Session-Id`, `Content-Type`, `Accept`, `Content-Length`, `Host`) are rejected at write time. See `internal/plugin/oauth.HeaderSetCreds`.
- `basic_auth` — HTTP Basic Auth. Storage shape: `{username, password}`. See `internal/plugin/oauth.BasicAuthCreds`.
- `oauth2_authcode` — host-side authorization code flow (see §9.2).
- `oauth2_clientcred` — host-side client credentials flow (see §9.2).

**`basic_auth` is a stepping stone.** It exists to integrate with legacy enterprise services that have not migrated to OAuth or API-key headers. New plugin authors should prefer `static_api_key` or `oauth2_authcode`. Gleipnir does not redact the basic-auth password from outbound HTTP request logs — operators are responsible for minimising log retention when basic_auth is in use.

Out of v1: mTLS, SAML, Kerberos, custom strategies. Flagged as v2 candidates when concrete enterprise demand surfaces.

### 9.2 OAuth dance location

Host-side orchestration. Manifest declares the strategy; Gleipnir owns the callback at `<public_url>/api/v1/admin/plugins/oauth/callback`. Tokens are encrypted in `plugin_instances.credentials_encrypted` (generalizes the ADR-039/040 patterns).

**Callback state protocol.** The OAuth `state` parameter is an HMAC-signed JSON envelope `{instance_id, nonce, expires_at, return_url}`, signed with `GLEIPNIR_ENCRYPTION_KEY`. The callback handler verifies the HMAC, checks the 10-minute expiry, and consumes the nonce against a server-side one-time table (CSRF protection). If `public_url` changes mid-dance, in-progress states fail signature verification and the operator must restart the OAuth flow — surfaced as a polite error explaining the cause. Re-authorization after `public_url` change is also required for any instance whose previously-registered callback URL no longer resolves; admin UI surfaces affected instances with a "Re-authorize" prompt.

Plugin authors bake `client_id`/`client_secret` defaults into the manifest. Instance config can override for power users with private apps. The manifest **must** declare defaults presence (informed consent at install). Plugin version bumps that change OAuth defaults trigger admin re-approval (falls out of the dropin distribution + manifest material change machinery).

### 9.3 Token refresh

Scheduled scanner (default `GLEIPNIR_OAUTH_REFRESH_INTERVAL=5m`, lead time 15m) refreshes tokens expiring within window. New scanner alongside existing approval/feedback ones in `internal/timeout/`. Refresh failure → instance marked unhealthy with admin "Re-authorize" button (matches the ADR-040 pattern).

### 9.4 Credential delivery to plugins

- Pull-only — plugin calls `GetCredentials` per RPC. **Plugins are forbidden from caching credentials.**
- Reads → slog via existing logctx correlation (high-volume, no DB writes).
- Mutation events (issue / refresh / refresh-fail / revoke) → `plugin_audit_events` table.
- `run_steps` is **not** polluted with credential events (see §12.3 audit discipline).

## 10. Versioning and compatibility

### 10.1 Version axes

Per-service versioning with four independent axes: `TriggerService`, `ToolService`, `ChannelService`, `HostAPI`. The manifest declares which version of each it implements. Shared protobuf messages live in `gleipnir.plugin.common.v1` and bump as part of any service that uses them — they are not independently versioned.

### 10.2 What's "breaking"

| Class | Examples | Treatment |
|-------|----------|-----------|
| **Additive** | New optional fields, new RPCs | Non-breaking. New enum values are breaking unless plugins are required to handle a mandatory `UNKNOWN = 0` first value (lint-enforced on `.proto` files). |
| **Behavioral** | `Notify` semantics shift | **Always** a major bump. No "soft semantic shift." If `Notify` changes, that's `NotifyV2`. |
| **Removal** | RPC or field deletion | **Two major versions of deprecation.** v1 deprecates X with warnings; v2 still ships X with warnings; v3 removes X. Generous because operators don't watch release notes weekly and community plugins go dormant. |

**Plugin version vs service version.** Plugin authors set their plugin's own SemVer (`slack v2.5.0`) independently of the service versions their binary implements (`ToolService v1`, `ChannelService v1`, …). Both appear in the manifest. The plugin's own version is informational and authored by the plugin author; the service version drives compatibility and the §10.2 deprecation window above. Bumping plugin version is **not** a §5.4 material change unless services or capabilities also change.

### 10.3 Handshake

Manifest is the install-time authority for UX, gating, and consent screens. `Handshake/v1` (the one immortal API, never bumps) at every plugin start verifies the binary actually implements what the manifest claims. Handshake-declared capabilities must be a subset of manifest-declared. Mismatch → kill, audit, mark unhealthy. Per-instance version pinning recorded in `plugin_instances.handshake_versions`.

### 10.4 HostAPI multi-version side-by-side

**Not in v1.** Single HostAPI version per running host. Plugins must implement the host's current HostAPI version. Major bumps follow the §10.2 deprecation window. Asymmetric to service-side multi-version is intentional: plugins evolve independently and may go dormant (long support windows needed); HostAPI evolves with the host (operators upgrade as a unit). Revisit if a paid third-party storefront materializes.

## 11. Admin UI

### 11.1 `/admin/plugins` list view

Two-pane page. Left list = one card per **plugin** (binary+manifest pair), not per instance. Card shows:
- Name, version
- Declared services as badges (Tool / Trigger / Channel)
- Instance count
- Aggregate health (worst state across instances)

### 11.2 Plugin detail view

Per-plugin: list of instances + "Add instance" button. Per-instance link drills to the edit form.

### 11.3 Instance edit form

Tabs:
1. **Config** — rendered from manifest's instance `config_schema` (reflection-derived JSON Schema)
2. **Credentials** — driven by manifest's auth strategy. Static keys / header sets show input fields, write-only over the API. Read returns key names only (mirrors ADR-039 webhook secret pattern). OAuth strategies show "Authorize" button + last-refresh timestamp.
3. **Subscriptions** — appears only if the plugin declares triggers. Admin sets coarse watch scope (e.g. "Watch channels: `#incidents`, `#ops`"). Per-policy bindings still happen in the policy editor.

### 11.4 Install flow

Filesystem dropin + fsnotify auto-detect. New manifests land in **"Pending review"** state requiring admin click-through to activate. The review screen surfaces:
- Declared services
- Tier-2 capability declarations
- OAuth defaults presence (if any)
- TOFU pubkey acceptance
- SBOM badge if a `sbom:` field is present in the manifest

### 11.5 Uninstall semantics

Two-step lifecycle per instance:
- **Deactivate** (soft, reversible). Stop subprocess, refuse new invocations, DB row stays. In-flight runs continue calling the existing generation until completion. Audiences and policy bindings that reference this instance start showing unhealthy chips.
- **Remove** (hard, gated). Available only when ALL three are zero: in-flight references, policies with subscribed triggers from this instance, audiences with this instance configured. Blocking dialog lists offending dependents with deeplinks. **No cascade** — admin must explicitly decouple first.

Removing the plugin (binary + manifest, not an instance) requires all instances be removed first.

### 11.6 Relationship to `/admin/mcp`

Siblings, not merged. Both live as separate top-level items under `/admin/`. List-view layout style aligns where it can; detail view legitimately diverges (plugins need multi-instance, three service types, OAuth, signing status).

### 11.7 Audience management

New page at `/admin/audiences`. Admin/operator edit; auditor read. Audiences are shared resources referenced from multiple policies by name. Editor banner shows resolved routing live ("Notifications fan out to: A, B, C" / "Requests routed to: A").

**Save guard:** when an audience referenced by ≥1 policy is edited, the save dialog lists affected policies (linked) and requires confirmation (`X policies will receive new routing`). New audiences (zero references) save without prompt. Audiences with active in-flight runs that reference them are flagged separately ("Y in-flight runs affected — change applies to subsequent steps only; in-flight Channel Requests already issued continue to resolve against the previous routing").

### 11.8 Policy editor changes

New `AudienceSection` component under `FormMode/`, with a single audience dropdown. "+ New audience" link deep-links to `/admin/audiences/new` preserving policy draft state on return. Trigger picker becomes the unified flat list (built-ins + plugin event kinds; no separate "subscribed" cascade).

## 12. Observability

### 12.1 Logging

- Tier-1 `Log(level, msg, attrs)` Host RPC for production logging
- Stdout/stderr also captured per-line, prefixed `plugin=<name> instance=<instance>` — fallback for panics outside an active call only
- All log paths flow through Gleipnir's slog and inherit logctx correlation

### 12.2 Metrics

- `gleipnir_plugin_*` namespace mandatory; plugins cannot escape the prefix
- Auto-injected labels: `plugin`, `instance`
- Host emits its own observability metrics: `gleipnir_plugin_rpc_duration_seconds{rpc, plugin, instance}`, `gleipnir_plugin_process_rss_bytes{plugin, instance}`, etc.
- OpenTelemetry / distributed tracing is out of v1 (Gleipnir core has no OTEL today)

### 12.3 Audit-vs-trace discipline

Two audit substrates with distinct purposes:

| Substrate | What goes here |
|-----------|----------------|
| `run_steps` | LLM-relevant operations only: `tool_call`, `tool_result`, `feedback_request`, `feedback_response`, plus existing v1 step types. **Visible to the LLM.** |
| `plugin_audit_events` | Operational/admin-relevant events: install, manifest changes, signature verification outcomes, key rotations, credential issue/refresh/revoke, unauthorized RPC attempts, deactivate/remove. **NOT visible to the LLM.** |

Schema for `plugin_audit_events`:
```sql
CREATE TABLE plugin_audit_events (
  id INTEGER PRIMARY KEY,
  plugin_instance_id INTEGER NULL,  -- nullable for plugin-level events
  event_type TEXT NOT NULL,
  severity TEXT NOT NULL,
  actor_user_id INTEGER NULL,        -- nullable for system-driven events
  payload_json TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_pae_instance_created ON plugin_audit_events(plugin_instance_id, created_at);
CREATE INDEX idx_pae_event_created ON plugin_audit_events(event_type, created_at);
```

### 12.4 Substrate-side errors

Plugin-author intent determines surfacing:
- **Per-call** substrate errors (e.g. Slack 403 on a single tool call): plugin returns an error from the service RPC; host writes `tool_error` step
- **Persistent / instance-level** errors (auth expired, sustained 5xx): plugin calls `SetHealthState(state, detail)`; host surfaces in admin UI
- **Protocol-level** crashes / hangs: covered by the failure-mode policies in §13.7

## 13. Resource limits and process management

### 13.1 Memory

- **GOMEMLIMIT advisory** set on plugin process start (manifest-declared or 256MB default). Soft, plugin-side.
- **Container sizing:** operators should reserve `host_baseline + sum(plugin_GOMEMLIMIT) × 1.2` for the API container. The `/admin/plugins` page header shows aggregate plugin RSS in real time (`Plugin memory: 412 MB across 3 instances`, sampled every 30s via `gleipnir_plugin_process_rss_bytes{plugin, instance}`). Per-instance breakdown is available by clicking the summary. The gauge is also available for external Prometheus alerting.
- **Circuit-break-on-OOM**: SIGKILL'd plugin processes count toward the failure-mode circuit breaker (§13.7).
- Hard cgroup-per-subprocess deferred to v2. Implementing it inside the already-containerized API container is non-trivial Linux plumbing; first-party + admin-installed v1 plugins do not justify the complexity.

### 13.2 Concurrent calls

Per-plugin `max_concurrent_calls`, default 50, per-instance configurable. Host-side semaphore at the dispatcher. Beyond limit, host queues with a depth cap + per-call deadlines. Mostly affects ToolService — Channel `Notify` is fire-and-forget, Channel `Request` is async, Trigger `Start` is one persistent stream.

### 13.3 Generation stacking

At most **1 draining old generation** per plugin. A second hot-reload while the previous old generation is still draining queues the new generation start until the old drains or hits a 60s force-kill grace. Prevents N-deep stacking under rapid reloads.

### 13.4 Shutdown

On host SIGTERM: send SIGTERM to all plugin subprocesses, 10s grace, SIGKILL stragglers. In-flight runs are covered by Gleipnir's existing graceful-shutdown semantics (interrupted state per ADR-038). No new mechanism — wire plugin processes into the existing shutdown sequence.

### 13.5 Auto-restart

Auto-restart with exponential backoff capped at 5 minutes. Standard gRPC health protocol (`grpc.health.v1.Health`); ping every 30s; 3 missed → restart.

### 13.6 gRPC deadlines

| RPC | Deadline |
|-----|----------|
| Tool call | 30s (matches `GLEIPNIR_MCP_TIMEOUT`) |
| Channel `Notify` | 10s |
| Channel `Request` (pre-ack) | 5s |
| `ListTools` | 5s |
| `Cancel(call_id)` | 5s — see §13.8 |
| Trigger `Start` | None (long-lived stream; health pings cover liveness) |

### 13.7 Failure-mode policy

- Output validated against manifest JSON schemas. Failures → `plugin_protocol_error` step (not passed to LLM).
- Tool plugin crash → agent gets a `tool_error` step (matches MCP failure today).
- Channel Request **pre-ack** failure → run fails fast (`feedback_dispatch_error`).
- Channel Request **post-ack** failure → existing feedback timeout machinery.
- **Circuit break: 5 failures / 10 min** → admin must manually restart.

### 13.8 Cancellation

Cancellation propagation is default-on. The `call_id` issued in §8.5 doubles as the cancellation handle: when a run is cancelled (or any individual call needs to be aborted), the host issues `Cancel(call_id)` for every in-flight call_id originating from that run. Plugins MUST stop work and return a result (or a cancellation error) within the §13.6 5s deadline; host force-disconnects the call on timeout. Plugins are expected to honor cancellation by aborting outbound work (downstream HTTP cancelled via Go context, etc.). Lifecycle events (run-started/completed/failed pushed to plugins) are opt-in via manifest declaration.

## 14. Developer experience

### 14.1 SDK

Subdir module in main repo at `/plugin-sdk/` with its own `go.mod`. Module path `github.com/felag-engineering/gleipnir/plugin-sdk`. Same-repo coordination during v1 churn; can split to a dedicated repo at v1.0 GA. Strawman layout: `proto/`, `gen/`, `manifest/`, `serve/`, `testing/`, `examples/`.

### 14.2 CLI: `gleipnir-plugin`

A separate CLI shipped from the SDK module (`/plugin-sdk/cmd/gleipnir-plugin`). Decoupled from `gleipnirctl` (which stays a server-admin CLI). Subcommands:

| Command | Purpose |
|---------|---------|
| `new` | Scaffold a new plugin (see §14.6) |
| `validate` | Validate manifest + binary against schemas |
| `keygen` | Generate a Minisign keypair |
| `sign` | Sign a binary + manifest |
| `package` | Build, sign, and tar a release bundle |
| `run` | Local REPL/TUI dev mode against a fake host |
| `gen-manifest` | Invoke `<binary> --emit-manifest`, write deterministic YAML |

### 14.3 Manifest authoring

**Code-first, generated.** Author declares tools / triggers / channels / event_kinds in Go via SDK builder types. JSON Schemas are derived from Go struct types via reflection (e.g. `jsonschema:"required"` tags). `gleipnir-plugin gen-manifest` invokes `<binary> --emit-manifest`, writes deterministic YAML. The YAML is committed and is the canonical artifact (signing hashes raw bytes; emitter must produce byte-identical output for the same Go declarations — sorted keys, fixed indent).

### 14.4 Testing harness

Both:
- **Unit-test library** (`plugin-sdk/testing.NewFakeHost(opts...)`) for assertions
- **Interactive `gleipnir-plugin run` REPL/TUI** for exploration

Same fake-host implementation backs both. Covers all Tier-1 Host RPCs + stubbed Tier-2; captures audit/event/metric calls for assertion. Does NOT simulate signature verification, version mismatch, or a real LLM/SQLite. Optional `--scenario script.yaml` for replayable tests.

### 14.5 Signing tooling

- Bundled Go Minisign library (no external `minisign` binary dep)
- File-based keys default, stored under `~/.config/gleipnir-plugin/keys/`
- CI mode via `GLEIPNIR_PLUGIN_SIGNING_KEY` + `GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE` env vars or `--key-stdin`
- `gleipnir-plugin package` produces `<name>-<version>.tar.gz` containing binary, `manifest.yaml`, `<name>.minisig`, `signing.pub` (shipped inside bundle for TOFU capture), and optional `sbom.cyclonedx.json`
- `package` refuses without `--key` or explicit `--unsigned`. `--unsigned` only loads on hosts with `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true`
- `gleipnir-plugin run` does no sign/verify — pure dev convenience

### 14.6 `new` scaffold

Default invocation `gleipnir-plugin new <name>` produces:

```
<name>/
  go.mod                 # module name from arg, requires plugin-sdk
  main.go                # serve.Serve() + --emit-manifest mode wired
  manifest.go            # builder-style declarations
  service.go             # impl with TODO bodies + example error returns
  service_test.go        # uses plugin-sdk/testing.NewFakeHost
  README.md              # quickstart: build, validate, run, package
  Makefile               # build, test, validate, package, sign targets
  .gitignore             # ignores keys, binaries, .minisig
```

**No committed signing key in scaffold** (security hazard).

`--kind` variants:
- `tool` (default) — single ToolService skeleton with one example tool
- `channel` — ChannelService with both Notify and Request stubs (commented "delete what you don't implement")
- `trigger` — TriggerService with one EmitEvent example
- `combo` — all three (Slack/kitchen-sink path)

### 14.7 Reference plugins

Two first-party plugins + four SDK examples, all in the main repo for v1 (split to separate repos at v1.0 GA):

- **`/plugins/slack/`** — kitchen-sink validation: all three kinds, OAuth2 authcode, real day-one value
- **`/plugins/ntfy/`** — minimum-surface: channel-only Notify, ~100 LOC end-to-end readable
- `/plugin-sdk/examples/`: `minimal-tool/` (committed). Additional `minimal-trigger/`, `minimal-channel/`, and `static-api-key/` examples are scaffolded on demand via `gleipnir-plugin new --kind {trigger,channel,...}` rather than committed.

Out of v1: GitHub plugin, email/SMTP, Discord/Telegram/Matrix (community territory).

### 14.8 Supply-chain extras

- **SBOM**: optional `sbom: <relative-path>` manifest field, CycloneDX JSON preferred. Gleipnir does NOT parse it — surfaces "SBOM available" badge in admin UI and serves the file at a known endpoint. Zero enforcement cost; seeds the social norm for storefront-era requirements.
- **Build attestation, SLSA provenance, vulnerability scanning, reproducible-build verification**: all out of v1. Encouraged in docs only (`-trimpath`, `CGO_ENABLED=0`, pinned toolchain). No host-side rebuild verification.

Honest framing: v1 supply-chain story is Minisign tamper-evidence + optional SBOM pointer. v1 protects against modified binaries, not malicious-but-correctly-signed binaries. The `plugins:install` admin gate is the human firewall until storefront era.

## 15. Build sequence and rollout

### 15.1 Build order

Each step is shippable independently:

1. **Proto + SDK skeleton.** Just enough to compile a hello-world plugin. Locks contracts.
2. **In-app feedback Channel refactor.** Pure internal refactor of `internal/execution/agent/feedback.go` — `FeedbackHandler.Wait` factored into a Channel-routing dispatcher; the in-process `inAppChannel` becomes a Channel implementation; `runsHandler.SubmitFeedback` calls `inAppChannel.Resolve(request_id, body)`. **Behavior-neutral**, no flag, no externally visible change. Validates the Channel abstraction before betting plugins on it. **Ship gate:** the existing feedback test surface (`internal/feedback/...`, runs-feedback API tests, and the SSE feedback E2E) passes byte-identical with both implementations; a parallel-implementation harness proves equivalence on a curated trace before old code is deleted.
3. **Plugin loader + signature verification + hot-reload + manifest snapshot.** Trust infra before any plugin runs.
4. **ToolService end-to-end** with `plugin-sdk/examples/minimal-tool/`. Forks at the last MCP-dispatch hop.
5. **ChannelService end-to-end** with **ntfy** plugin + audiences admin UI.
6. **TriggerService end-to-end.** Hardest because of `subscribed` trigger + structured binding evaluation. Needed for Slack.
7. **Auth subsystem** (OAuth authcode + refresh scanner). Bundled with...
8. **Slack plugin.** Kitchen-sink validation of all three services + OAuth.
9. Admin UI iterates throughout.

### 15.2 Feature flag

Single global env var `GLEIPNIR_PLUGINS_ENABLED`, now `true` by default (flipped on after a stable cycle — see lifecycle below). Loader/manager doesn't start unless on. When off: `/admin/plugins` returns 404, audience editor hidden, plugin instance pickers empty.

**The flag is a temporary rollout mechanism, not a permanent config knob.** Lifecycle:
1. ~~Ships off-default in release N~~ (done)
2. **Flips on-default in release N+1 after a stable cycle** ← current state; set `GLEIPNIR_PLUGINS_ENABLED=false` to opt out for one more release
3. **Flag removed entirely** in release N+2

The step-2 in-app feedback refactor lands independently of the flag (behavior-neutral). The flag only gates external plugins, not the internal `inAppChannel`.

**Subprocess spawn on instance creation.** When an admin creates a plugin instance via the API, the handler immediately calls `StartByPluginID` to spawn the subprocess — no server restart required (same fire-and-forget pattern as the post-install hook).

### 15.3 Default-on success criteria

Zero P0 issues from opt-in users for a full minor cycle, both first-party plugins (Slack + ntfy) running stably in homelab.

## 16. ADRs to write

Rough mapping; numbers assigned at write time. Each is in scope for the umbrella plugin work.

- Plugin system architecture (umbrella ADR)
- Channel routing model — supersedes ADR-031's in-app implementation detail; ADR-031's first-class-feedback principle stands
- Subscribed trigger type — refines existing trigger-model
- Plugin signing & TOFU trust
- HostAPI versioning policy
- Plugin observability surface
- Audit-table split: `run_steps` vs `plugin_audit_events`

## 17. Deferred to v2 (explicit non-scope)

- Hard cgroup-per-subprocess resource isolation
- Sigstore-keyless signing / identity attestation (depends on plugin storefront)
- Per-policy and per-user credential scoping for plugin instances (`user_credentials` mode)
- HostAPI multi-version side-by-side support
- Rotation certificates for signing key bumps
- Per-event-type audience routing overrides
- Multiple triggers per policy
- JSONPath backport to PollConfig "test against sample" UX
- Lifecycle hook plugins, LLM provider plugins
- OpenTelemetry / distributed tracing
- Per-tool runtime budgets
- `RecoverChannelRequests` RPC (plugin author owns substrate↔request_id correlation in v1)
- Build attestation / SLSA provenance / vulnerability scanning
- Curated plugin registry / storefront
- Multi-arch resolution for plugin binaries
- Upload-via-UI install flow

**v1 plugin shapes blocked by deferred items.** Plugin substrates that conceptually require per-user credentials (Gmail per individual, personal calendars, individual GitHub PATs) are blocked until v2's `user_credentials` mode lands. v1 plugins shipping as one-shared-credential-per-instance (Slack workspace bot, ntfy server, GitHub org token) work fine. Plugin authors targeting per-user-credential substrates should wait for v2 rather than ship workarounds.

## 18. Glossary

- **Plugin** — A binary + manifest pair shipped by an author. Identified by name + version.
- **Instance** — A configured deployment of a plugin (carries credentials and per-deployment config). One plugin → many instances.
- **Generation** — A specific subprocess incarnation of an instance. Replaced on hot-reload.
- **Audience** — A named, ordered list of channel entries that a policy targets when it needs to notify or request operator input.
- **Channel** — A plugin's `ChannelService` implementation supporting `Notify` and/or `Request`.
- **Subscribed trigger** — Internal trigger type for policies bound to a `(plugin_instance, event_kind)` pair with a structured binding filter.
- **TOFU** — Trust on first use. The first valid `signing.pub` seen at install becomes the pinned trusted key; updates must verify against it.
- **Material manifest change** — A change to declared services, capabilities, OAuth configuration, or pubkey claim. Triggers admin re-approval on hot-reload. Cosmetic changes (description, version string) do not.
- **Tier 1 / 2 / 3** — Capability tiers for Host RPCs. Tier 1 is always on; Tier 2 is manifest-declared and admin-approved at install; Tier 3 is out of scope for v1.

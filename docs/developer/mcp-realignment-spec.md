# MCP 2026 Realignment — Target Architecture Specification

**Target release tag:** `v0.2.0-alpha`
**Status:** Accepted 2026-07-30 (design consensus; implementation not started)
**ADRs:** ADR-053 through ADR-060 (indexed in [ADR_Tracker.md](ADR_Tracker.md); this document is their shared body)
**Supersedes in part:** ADR-041 (plugin substrate), ADR-042 (service versioning axes), ADR-044 (Request dispatch mechanics), ADR-047 (process log pipe), ADR-050 (plugin-sdk service seams — deleted with the gRPC substrate), ADR-051 (tool-call pool absorbed into the unified MCP client; the trigger/channel separation principle survives in new form)
**Leaves intact:** ADR-001, ADR-008, ADR-017 (semantics extended, see §10), ADR-026, ADR-031, ADR-045 (trust model unchanged, packaging updated), ADR-046, ADR-048, ADR-049, ADR-052

---

## 1. Background

The MCP `2026-07-28` specification (final 2026-07-28; RC 2026-05-21) restructured the
protocol in ways that overlap most of the reasons Gleipnir's bespoke gRPC plugin
substrate exists:

| MCP change | SEP | Relevance |
|---|---|---|
| Stateless core: no sessions, no `Mcp-Session-Id`, no `initialize` handshake; version/capabilities/clientInfo in `_meta`; mandatory `server/discover` | SEP-2567, SEP-2575 | MCP client rework; reserved-header list update |
| Multi Round-Trip Requests: `tools/call` may return `resultType:"input_required"` with `inputRequests` (elicitation) + opaque `requestState`; client fulfills and retries | SEP-2322 | Tool-initiated human-in-the-loop becomes a *protocol* pattern with the host as mediator |
| Tasks extension (`io.modelcontextprotocol/tasks`): durable task handles, `tasks/get` polling, `tasks/update` for input, `tasks/cancel`, TTL + poll interval | SEP-2663 | Standard home for long-running operations and human waits; maps ~1:1 onto channel `Request` |
| Sampling, Roots, Logging deprecated (12-month window) | SEP-2577 | Validates ADR-026 (agent loop owns the LLM); Gleipnir never declares sampling |
| Extensions framework: reverse-DNS ids, negotiation via `extensions` capability maps | — | Sanctioned way to add custom surfaces (`io.gleipnir/*`) instead of a parallel protocol |
| Tool schemas widened to full JSON Schema 2020-12 (`oneOf`/`anyOf`/`allOf`, `$ref`) | SEP-2106 | Affects ADR-017 scoping and LLM provider translation (§10) |
| `Mcp-Method`/`Mcp-Name` headers required; `ttlMs`/`cacheScope` cache hints; deterministic `tools/list` ordering; error-code renumbering | SEP-2243, SEP-2549 | MCP client compliance work (§11) |
| **Not shipped:** arbitrary server-initiated events. `subscriptions/listen` covers only list-changed + resource subscriptions. The Triggers & Events WG (chartered 2026-03-24) owns the future mechanism | — | Gleipnir's trigger gap remains real; we fill it with our own extension and propose it upstream (§5) |

This document specifies the target architecture that realigns Gleipnir onto the new
protocol, replaces the go-plugin gRPC substrate, and defines what stays
Gleipnir-proprietary.

## 2. Goals and non-goals

**Goals**

1. One MCP client stack for everything Gleipnir talks to, with two trust tiers.
2. Plugins become **signed, containerized MCP servers**: any MCP server + manifest +
   signature = plugin. Language-agnostic; the CGO static-build constraint dies.
3. Tool-initiated human-in-the-loop via MRTR + Tasks, routed through audiences,
   without the agent in the loop.
4. Event triggering via a Gleipnir-authored MCP extension, built to be proposed to
   the MCP Triggers & Events WG.
5. Capability enforcement extended to the network layer (egress containment,
   east-west isolation).
6. User-scoped plugin identity and preferences (Tiers 1–2, §9).

**Non-goals (named, deliberate)**

- **Tier 3 per-user credentials** ("act as me"): deferred. Runs are policy-scoped;
  "whose authority does a run act under" is its own future design (same deferral as
  ADR-039/ADR-040 per-user scoping). URL-mode elicitation is the eventual mechanism.
- **Quorum / two-person approvals**: the Request contract resolves on the first
  authorized actor. The task-based contract leaves room to add quorum later; it is
  out of scope now.
- **A2A**: parked as an advanced future feature. Noted: A2A's task lifecycle
  (`submitted/working/input-required/auth-required/completed/failed/canceled/rejected`)
  maps ~1:1 to Gleipnir run states; avoid vocabulary drift.
- **Run resurrection**: required for "waits survive host restart" in full, deferred
  to its own epic (§13). Until it lands, a restart still interrupts waiting runs;
  channel-Request tasks (which live outside runs) do survive via re-poll.
- **MCP Sampling**: never declared, never adopted (deprecated upstream).
- **MCP Apps** (SEP-1865, iframe-rendered server UIs): parked. In-app approval and
  feedback rendering stays native; revisit only against a demonstrated need.

## 3. Architecture overview

```
                        ┌────────────────────────────────────────────┐
                        │                 Gleipnir host              │
                        │  agent runtime · audiences · binding/dedup │
                        │  audit · policies · reconciler · host API  │
                        └───────┬───────────────────────────┬────────┘
              one MCP client    │                           │  host API (gRPC,
              stack (2026-07-28,│                           │  internal net + instance token,
              version-straddling)                           │  managed plugins only)
                        ┌───────┴────────┐          ┌───────┴────────┐
                        │ External MCP   │          │ Managed plugin │
                        │ servers        │          │ = signed,      │
                        │ (standard MCP: │          │ containerized  │
                        │ tools, tasks,  │          │ MCP server     │
                        │ elicitation)   │          │ + extensions   │
                        └────────────────┘          └────────────────┘
```

**Two trust tiers, one protocol.**

- **External MCP servers** (the existing registry, ADR-039/040): standard MCP —
  tools, tasks, elicitation routed through audiences. No host API, no extensions
  (initially; see §5 extensibility).
- **Managed plugins**: MCP servers whose lifecycle Gleipnir owns. They additionally
  get: container management with egress containment (§7), the host API (§8), the
  `io.gleipnir/events` extension (§5), identity/user-scoping participation (§9),
  and credential injection from the existing OAuth/credential manager.

The bespoke dispatch path (`internal/plugin/dispatch` pool) collapses into the
shared MCP client with per-server concurrency/queue settings. `GLEIPNIR_MCP_TIMEOUT`
and related knobs apply uniformly.

## 4. Capability profiles (ADR-053)

A managed plugin is **any MCP server + manifest + signature**. Everything beyond
baseline is an optional, independently specified **profile**. The host lights up UI
and routing surfaces per declared profile. Profiles exist to prevent overfitting the
design to any single integration (Slack is the first implementation and the proving
ground; it validates the contracts but does not define them).

| Profile | Contract | Minimum obligation |
|---|---|---|
| **Tool provider** | Baseline MCP. | None — any existing ecosystem MCP server qualifies with zero code changes. Wrapping + manifest + signature yields containment, lifecycle, audit. |
| **Event source** | Implements `io.gleipnir/events` (§5). | `events/discover` + `events/listen`. **No reserved field names with fixed semantics** — `mention_only` becomes an ordinary boolean field the Slack plugin declares, not spec vocabulary. |
| **Human channel** | Notify tool convention + Request-as-task contract (§6.4). | Request task input `{message, options[], schema?}` → result `{option_id \| content, actor_external_id}`. Declares an **actor-assurance level** (§4.1). |
| **Identity provider** | Link methods + actor authorization participation (§9.1). | At least one link method, or none (profile omitted entirely). `external_user_id` is an opaque string namespaced by instance. |

Dynamic option lookup (today's `ConfigOptionsService`) becomes a tool convention
declared in the manifest rather than a dedicated gRPC service.

### 4.1 Channel assurance levels

Channels differ in actor-authentication strength: a Slack button click arrives
authenticated by Slack; an email reply's `From:` is forgeable. The human-channel
profile therefore declares an assurance level (`authenticated`, `weak`), and the
**host** — never the plugin — decides which request kinds a channel may resolve.
Default policy: low-assurance channels may answer *information* requests but a
*permission* request routed there falls through to the next audience entry. Audit
steps record the assurance level of the resolving channel (§6.6).

### 4.2 Design discipline

- Each profile spec is written **without the word "Slack"** in normative text;
  Slack appears only in an examples appendix beside divergent hypotheticals.
- **Rule-of-three paper validation** before a profile contract freezes: email
  (high latency, no buttons, weak actor auth), an event-only non-human source
  (Home Assistant / GitHub), and a vanilla ecosystem MCP server (tool-only) are
  walked through every contract. If a profile can't accommodate one without
  special cases, the profile is wrong, not the example.
- A **conformance checklist per profile** anchors CI (and pre-packages the events
  extension for the WG proposal, matching MCP's conformance-suite requirement for
  standards-track SEPs).

Known Slack-isms scheduled for removal in this realignment:
`users.slack_user_id` column + `GetUserBySlackUserID` query (→ §9.1),
`mention_only` as reserved binding vocabulary, "DM" as routing vocabulary
(→ neutral `delivery: direct | shared`).

## 5. `io.gleipnir/events` extension (ADR-054)

Fills the gap MCP explicitly deferred to the Triggers & Events WG. Negotiated via
the standard `extensions` capability map; reverse-DNS identifier; **SemVer and a
deprecation policy from birth** — the moment a third party implements it, Gleipnir
is a spec steward with the same obligations we expect of MCP itself.

**Methods**

- `events/discover` — returns event kinds, binding schemas (ADR-052 operator sets),
  and per-kind guidance text. Discovery lives in the protocol; the signed manifest
  *attests* which kinds a plugin may emit (drift between manifest and discovery is a
  health fault).
- `events/listen` — client-initiated long-lived stream (the shape the core spec
  blessed with `subscriptions/listen`). The client passes a subscription scope and a
  resume cursor; the server pushes events, each carrying source, kind, dedup key,
  sequence number, and payload. The server buffers durably; the client acks via
  cursor on (re)connect. Delivery is **at-least-once**; the existing
  `internal/plugin/dedup` store is the downstream that makes it effectively-once.
  Redelivery is application-level by design (core removed transport resumability).

**Core principle (exact wording matters).** *The event as control signal is
host-captured and never enters model context. Event payloads MAY reach model
context only as untrusted data, when a policy explicitly templates them into the
task prompt.* This is the control-plane/model-plane separation ADR-046 already
encodes, stated as protocol design.

**Scope.** Initially closed to signed managed plugins. Structurally extensible to
per-server admin opt-in for external MCP servers later (an explicit decision, not a
default). Delivery flows into the existing pipeline unchanged:
`events/listen` client → dedup → `GetSubscribedActivePolicies` → binding compile/
evaluate → `RunLauncher.LaunchWithConcurrency`.

**WG posture.** Implement first, then propose to the Triggers & Events WG
(incubation repo is pre-spec; the field is open). A webhook binding is documented
as a possible v2 of the extension for WG alignment; v1 is stream-first because our
deployment audience makes outbound connections, not inbound. If the WG lands a
different standard, we migrate once — from an MCP-shaped extension, not from gRPC.
See §14 for the contribution/patent boundary.

## 6. Human-in-the-loop (ADR-055)

Three HITL sources now exist, unified on audiences and the run state machine:

1. **Agent-initiated** (`gleipnir.ask_operator`, ADR-031) — unchanged.
2. **Policy-gated approval** (ADR-008) — unchanged: pre-execution, host-owned,
   hard guarantee.
3. **Tool-initiated** (new): an MCP server returns MRTR `input_required` or a task
   enters `input_required`. The host pauses the run (`waiting_for_feedback`),
   routes the request through the policy's audience, and on operator response
   retries the original call with `inputResponses` (or `tasks/update`). The agent
   never sees the exchange; its context shows `tool_call` → `tool_result`.

Tool-initiated permission is **distinct from** the ADR-008 gate — different actor
(the tool, mid-execution, cooperative vs the host, pre-execution, mandatory),
different step types, both may occur on one call and the trace shows the sequence.

### 6.1 Permission vs information requests

- **Convention:** an elicitation whose `requestedSchema` requests no fields is a
  consent-only ask — a **permission** request (approve/reject rendering, resolvable
  by `approver`+ roles, generalizing the #624 role gate). A schema with fields is an
  **information** request (form rendering, `operator`+).
- Managed plugins make it explicit via `_meta` `io.gleipnir/elicitation-kind`;
  the manifest may declare per-tool kinds.
- Upstream: propose a small additive SEP for a first-class `intent` field on
  `ElicitRequest`.
- Spec rules adopted: form mode never carries secrets (URL mode exists for that;
  in low-fidelity channels the URL is presented domain-highlighted as an explicit
  "open in browser" step). Elicitation messages are server-controlled text rendered
  as untrusted content everywhere.

### 6.2 Abuse controls — three hard caps, no heuristics

1. **Per-run elicitation budget** — a sibling of `max_steps`, policy-configurable,
   fail-closed (budget exhausted ⇒ the call fails structurally; the run continues
   or fails per policy).
2. **Size caps** on persisted `requestState` (bytes) and `inputRequests`
   (count + bytes); oversize is rejected as a structural error.
3. **Per-server rate limit** on `input_required` responses — token bucket, the
   EmitEvent limiter pattern (injectable clock, `AllowN(timeNow(), 1)`).

Rationale: repetition fatigue-trains approvers (the worst possible operator
conditioning); debounce spaces the attack out without capping it.

### 6.3 Timeout authority precedence

Three clocks can govern one wait: Gleipnir's feedback/approval timeout, the
server's task TTL, and any TTL inside MRTR `requestState`. Precedence:

1. **Gleipnir's policy timeout is authoritative for the human leg.** On expiry the
   host fires `tasks/cancel` (or abandons the retry) and resolves the run per the
   existing timeout semantics.
2. **Server-side TTLs are weather.** Expiry is surfaced as a distinct,
   well-explained failure — and usually recovered automatically (§6.5).
3. Audiences display the **effective** deadline (min of the applicable clocks).

### 6.4 Channel `Request` = durable task

`ChannelService.Request` and its machinery — `plugin_pending_requests`, the
in-memory waiter, the stranded-row reclaim scanner, `RequestTerminated` (#625) —
are replaced by the Tasks extension: the host calls the channel plugin's request
tool, receives a durable task handle (persisted in DB), polls per the server's
interval, and resumes polling after restart. `tasks/cancel` covers termination.
The **in-app** channel (`gleipnir.in-app`) is modeled on the same internal task
lifecycle so every Request has one shape regardless of route.

Actor authorization (#624) inverts from write-then-refuse to **pre-check**: the
plugin's click handler calls the `AuthorizeActor` host RPC first; unauthorized ⇒
ephemeral reply, task stays running; authorized ⇒ plugin completes the task with
`{option_id, actor_external_id}`.

### 6.5 Server TTL expiry: auto-retry with answer replay

MRTR is stateless, so "start over" is always available. When the human's answer
arrives after the server's state expired: re-issue the *original* request; when the
fresh `InputRequiredResult` asks the **identical** question (message + schema hash
equality), replay the stored answer against the new keys and `requestState`
automatically — the human never sees the hiccup. Any difference re-prompts the
human with context. Pre-elicitation side effects re-running is the server's MRTR
idempotency obligation, not ours. Managed-plugin SDK defaults (durable tasks,
generous TTLs) make this path rare.

### 6.6 Audit

Decision steps record: channel, resolving actor, identity link-verification
method, and channel assurance level — the difference between "an approval
happened" and Article-14-grade oversight evidence. Step-type additions (e.g.
`tool_permission_request` / `tool_input_request` alongside the existing
`approval_request` / `feedback_request`) keep the ADR-046 split intact: MRTR
plumbing details are operational; the agent-visible trace shows only the tool
call and its result.

## 7. Container substrate (ADR-056)

Managed plugins run as **containers**, managed via the container-runtime socket
using the official Go Docker SDK (Docker-out-of-Docker; Podman's Docker-compatible
socket is fully supported).

**Socket posture** (in order):

1. **Recommended:** rootless Podman socket.
2. **Fallback:** Docker socket with the trust implication documented plainly
   (socket ≈ root on host; no socket-proxy truly fixes `create`-with-hostile-binds).
   Gleipnir self-constrains its create calls: no mounts beyond a per-instance
   volume, no added privileges, internal networks only.
3. **Escape hatch — manual mode:** the operator declares plugin containers in
   their own compose file; Gleipnir discovers by label and health-checks but never
   touches the socket. Manual mode is a supported first-class configuration, not a
   degraded one.

**Networking.**

- **Egress containment:** each plugin container attaches to an internal-only
  network by default. The manifest declares needed egress (e.g. `slack.com`);
  the admin consents at review time, exactly like tier-2 capabilities. "This
  plugin cannot reach the internet" becomes runtime-enforced — ADR-001 extended
  to the network layer.
- **East-west isolation:** **one dedicated internal network per plugin instance**
  (Gleipnir attached to all; each plugin only to its own). Without this, a
  compromised plugin could call a sibling's MCP endpoint directly, or anything on
  a shared network could invoke plugin tools with no audit and no capability
  check — an ADR-001 violation by topology.
- **Subnet management:** stock Docker default address pools exhaust at roughly 30
  networks. Gleipnir allocates explicit `/24` subnets per instance from a
  configurable base pool (250+ instances before trouble) and documents the
  `default-address-pools` daemon tweak. The shared-network-plus-inbound-auth
  variant is the documented unbuilt fallback if someone genuinely needs hundreds.

**Bundle format.** The drop-in UX is preserved: a bundle remains a Minisign-signed
tarball (ADR-045 TOFU flow unchanged) now containing the manifest plus an **OCI
image archive** loaded via the socket API. Digest-pinned images inside a signed
tarball; fully offline-capable; no cosign dependency. SBOM field maps to image
SBOMs.

**Reconciler.** Gleipnir accepts the mini-orchestrator role, bounded by one rule:
**level-triggered reconciliation only.** Desired state is rows in SQLite; the
reconciler lists managed containers by label, diffs, and converges one step —
never an imperative sequence that must complete. Crash mid-rotation is just
another state the next pass converges from. The reconciler owns: boot-time
convergence, generation rotation (start-new → health-gate → switch → drain →
stop, per-generation instance tokens), orphan cleanup, image GC, and subnet
allocation. Resource limits become enforced cgroup caps (memory/CPU per manifest
+ admin override); the RSS sampler's role is served by container stats.

**Health.** Liveness = container healthcheck + `server/discover` probe.
Self-reported health stays via the host API. Health becomes **per-capability**
(per profile, and per tool where the plugin reports it) rather than
instance-wide — the known "one missing OAuth scope marks the whole instance
unhealthy" defect is fixed here, not ported.

**Logs.** Container stdout/stderr is captured with instance labels (fallback and
pre-handshake panics); correlated structured logging stays on the host API `Log`
RPC (ADR-047 rationale unchanged — gRPC carries `call_id`/`run_id`/`policy_id`).

## 8. Host API (ADR-057)

The host API **stays**, for managed plugins only — it is what "managed" means.
Transport: **gRPC over the per-instance internal network, authenticated per-RPC by
the instance token** (the existing #202 interceptor machinery). Not UDS-via-volume:
the network option generalizes, avoids rootless-userns socket-permission
fragility, and the marginal attacker it admits (already inside the per-instance
network *and* holding a stolen token) defeats both designs equally.

**RPC inventory.** Kept: `GetInstanceConfig`, `GetCredentials` (standing
credentials for substrate connections — header injection can't cover streams),
`GetRunContext`, `EmitMetric`, `Log`, `SetHealthState` (now per-capability),
tier-2 `RunHistoryRead`/`UserDirectoryRead`. Removed: `WriteAuditStep` (the
`feedback_response` path is subsumed by task completion), `EmitEvent` (subsumed by
`events/listen`). New: `AuthorizeActor` (§6.4), `SubmitIdentityProof` (§9.1),
`GetUserConfig` (§9.2).

**Boundary rule (normative).** *Request/response addressed to a host service →
host API. Fire-and-forget facts for the trigger pipeline → events extension.* An
identity proof must never be able to launch a policy; an event never needs a
response.

## 9. User scoping (ADR-058)

### 9.1 Tier 1 — verified identity linking

`plugin_user_identities` table: `(instance_id, user_id, external_user_id,
link_method, verified_at)`. The **host** owns the pending-link state machine, code
generation, and expiry; the plugin provides only the medium-specific proof leg.
Link methods (manifest-declared, all optional):

- `dm_code` — the host calls a manifest-named ordinary tool ("deliver this code to
  this external user privately"); the user enters the code in Gleipnir. Proves the
  claimant can read the claimed identity's private messages.
- `inbound_code` — the user sends a code through the medium (e.g. a slash
  command); the plugin calls `SubmitIdentityProof(external_user_id, code)` and
  relays accept/reject. Stronger proof: the claimant demonstrably acts as that
  identity, authenticated by the medium.
- `admin_set` — the existing admin-managed mapping, as one more method.

Only **verified or admin-set** identities feed actor authorization. Unverified
self-assertion is disqualifying: it cannot escalate roles (roles come from the
Gleipnir account) but it corrupts actor attribution in the audit trail.
`users.slack_user_id` and `GetUserBySlackUserID` migrate into this table.

### 9.2 Tier 2 — user-scoped preferences

Manifest gains `user_config_schema` (same JSON Schema subset as `config_schema`;
`x-gleipnir-secret` and `x-gleipnir-options` work unchanged). Per-user, per-plugin
storage sibling of `config_json` (encrypted at rest, ADR-038 CAS, ADR-049
redact-on-read). Rendered in each user's own settings page by the existing
schema-form machinery.

**Invariants:** user config may **never grant capability** (role gates and policy
grants are untouched). Routing-affecting preferences use a small Gleipnir-defined
annotation vocabulary interpreted by the **host's** audience dispatcher
(`delivery: direct | shared` — channel-neutral, not "DM"); plugins read user
config only for their own presentation concerns.

**Enabled follow-on (not designed now):** audience entries targeting people/roles
("notify whoever holds approver, directly"), resolved against verified identities
at dispatch time.

## 10. LLM provider schema policy (ADR-059)

**Lossy presentation, exact enforcement.**

1. **Normalize at discovery: byte-level normalization only.** `internal/schemanorm`
   (#735) decodes with `UseNumber`, enforces input/depth/node bounds, and
   re-emits with object keys recursively sorted and HTML escaping disabled.
   That is the entire transformation — it does **not** resolve `$ref`, does
   **not** strip `$defs`, and does **not** flatten `allOf`. Three earlier
   designs attempted progressively more of that (bounded `$ref` inlining +
   `allOf` merge; then a never-merge `$ref`-substitution-or-`allOf`-wrap
   design) and three independent security reviews each closed one widening
   mechanism and found a new one: keyword relocation across a schema-object
   boundary (e.g. `additionalProperties` moving relative to `properties`),
   then `$ref` mis-resolution (`$id` changing the base URI a pointer
   resolves against, percent-encoded pointer segments), then
   narrowing-through-wrapping not composing under negation (`not`/`if`/
   `oneOf`) plus `$schema` being position-dependent dialect metadata that a
   relocation can silently change. The root cause in all three rounds was
   the same: JSON Schema has keywords whose meaning depends on **where they
   sit**, and any structural transformation moves something. Store the
   normalized bytes. The safety argument for byte-normalization alone is one
   line: JSON object members are unordered (RFC 8259), and no JSON Schema
   keyword depends on member order, so reordering cannot change what any
   validator accepts — `internal/schemanorm`'s package doc has the full
   account, including the three ways a naive decode/re-emit round trip
   *does* change meaning (duplicate keys, invalid UTF-8/lone surrogates,
   numeric re-rendering) and why each is rejected rather than repaired.
2. **Per-provider translation** in `internal/llm`: each wire declares supported
   schema features. This step now owns **both** `$ref` resolution **and**
   `allOf` flattening (moved down from step 1) — Anthropic passes nearly
   everything; Google's function-declaration subset (which has neither
   `$ref` nor `allOf`) gets a documented lossy flattening, including `oneOf`
   (discriminated `oneOf` → enum where possible; otherwise a permissive
   union with variants described in prose; `allOf` branches merged into one
   object). Lossiness/widening here is the declared, accepted policy,
   because it runs entirely downstream of enforcement. The UI marks affected
   tools "schema simplified for this provider".
3. **Enforcement is exact against the stored schema.** The host validates the
   agent's arguments against the schema exactly as stored — byte-normalized,
   otherwise untransformed — before dispatch, using the
   `santhosh-tekuri/jsonschema/v6` validator already in-tree, which resolves
   `$ref` and evaluates `allOf` (and everything else) natively and exactly.
   A mismatch returns a structural error the agent can correct. The LLM may
   see a simplified picture (step 2's translation); enforcement never does.
   Because step 1 performs no structural transformation, there is no
   "canonical schema is stricter than the source" case to document here —
   member reordering cannot change what the stored schema accepts.

**ADR-017 v1 scoping rule:** `params` blocks apply to top-level properties of the
stored (byte-normalized) schema. Composing the policy narrowing into the
stored schema as an ADDITIONAL `allOf` branch is **not** a narrowing by
construction: when the stored schema carries `unevaluatedProperties` /
`unevaluatedItems` at or above the scoped location, the appended branch's own
`properties`/`items` keyword produces an ANNOTATION that satisfies that
`unevaluated*: false` for a *different* `allOf`/`oneOf` branch — verified
against `santhosh-tekuri/jsonschema/v6`: a `oneOf` schema with
`unevaluatedProperties: false` rejects an instance under its stored form but
accepts it once a same-shaped `allOf` branch supplies the missing `properties`
annotation. Appending an `allOf` branch narrows *only* when the stored schema
has no `unevaluatedProperties`/`unevaluatedItems` anywhere at or above the
scoped location. Enforcement MUST therefore do one of:
- (preferred — correct regardless of what the stored schema contains) validate
  the stored schema and the policy narrowing as **two independent compiles**:
  an instance must pass the stored schema's own compiled validator AND a
  second validator compiled from just the narrowing constraint, rather than
  composing both into one document; or
- reject the policy at save time (fail closed) whenever the stored schema
  carries `unevaluatedProperties`/`unevaluatedItems` at or above the scoped
  location, and only use the single-document `allOf`-append when that is
  provably absent.
The POLICY-EDITOR UI half unions branch property names across `allOf`/`oneOf`
purely for DISPLAY (so an operator can see which properties exist to scope) —
that union is presentation only and is never what gets enforced. A policy that
scopes a property whose governance across branches cannot be determined is
**rejected at policy save** with a clear error — fail closed; loosen later
only against demonstrated demand.

The enforcement compiler (`jsonschema.NewCompiler()`) MUST be constructed with
a deny-all `URLLoader` (`c.UseLoader(...)`): `jsonschema.NewCompiler()`
defaults its loader to `FileLoader{}`, so a stored schema containing
`"$ref": "file:///..."` would otherwise cause the enforcement compiler to read
an arbitrary local file at compile time. A stored schema whose `$ref` escapes
the document should also be rejected at discovery.

## 11. MCP client compliance (2026-07-28)

Required regardless of every other decision; users' servers will straddle protocol
versions for the 12-month deprecation window.

- Per-server version negotiation: `server/discover` probe, pinned per registry
  entry; bilingual client across `2025-xx` and `2026-07-28`.
- `_meta` on every request (`protocolVersion`, `clientInfo`, `clientCapabilities`);
  `Mcp-Method` / `Mcp-Name` headers on POSTs; `resultType` handling (absent ⇒
  `"complete"` for older servers); error-code renumbering; no session handling for
  new-protocol servers. **As shipped (#762)**: `resultType` is decoded onto
  `mcp.ToolResult` and normalised, but never interpreted in `internal/mcp` — a
  non-`complete` value is data, not an error, left for a later milestone to
  consume. The error-code work is a consolidation of the package's existing
  constants into one registry plus the previously-unrecorded `-32021`; the
  `-32002` → `-32602` renumbering is `resources/read`-only, so it is inert for a
  client that speaks only `initialize` / `tools/list` / `tools/call` /
  `server/discover`.
- Reserved-header blocklist (ADR-039 / `internal/infra/headervalidate`): add
  `Mcp-Method`, `Mcp-Name`; retain `Mcp-Session-Id` through the window.
- `x-mcp-header` tool-parameter headers (SEP-2243): honored only through
  `headervalidate` (reserved list applies) and never permitted to override
  admin-configured ADR-039 auth headers. **As shipped (#747) the gate is
  deliberately stricter than this line**, because the name is chosen by the
  remote server and the value by the model: on top of `headervalidate`,
  `internal/mcp` applies its own `x-mcp-header`-specific denylist (hop-by-hop
  and proxy-control headers, `Authorization`/`Cookie`, the forwarding and
  client-IP families, method-override spellings, `User-Agent`) plus an
  `[A-Za-z0-9-]` name allowlist — the allowlist exists because CGI/FastCGI
  backends fold every non-alphanumeric byte to `_`, so `X-Api-Key`,
  `X-Api_Key` and `X.Api.Key` all collapse to one env var and the twin written
  last wins. A collision with a configured ADR-039 auth header fails the call
  closed with a visible error rather than relying on header ordering to make
  the admin value win (the ordering guarantee is retained as defense in depth).
  Honored only on the 2026-07-28 transport; legacy request shaping is unchanged.
- Capability declaration is a per-request enforcement seam: Gleipnir declares
  `elicitation` (form + url) only where the policy allows it, and **never**
  declares `sampling` — a server cannot request what the client doesn't declare.
- Adopt `ttlMs`/`cacheScope` hints (poll trigger, discovery caching); rely on
  deterministic `tools/list` ordering for prompt-cache stability.

**Operator-facing notes** for the shipped client half of §10 and §11 —
protocol probing and per-server pinning, the Tools-page protocol badge, the
"schema simplified for this provider" notice (§10 step 2), `x-mcp-header`
behavior, and the `ttlMs`/`cacheScope` discovery cache — live in
[docs/user/mcp-protocol-migration.md](../user/mcp-protocol-migration.md).

## 12. Migration: aggressive hard cutover

There are no known third-party plugins and no install base to strand (decision:
be aggressive while that is true). Therefore:

- The gRPC substrate (go-plugin, hostwire, ToolService/ChannelService/
  TriggerService, dispatch pool, pending-request waiter/scanner) is **replaced
  outright** in `v0.2.0-alpha`. No dual-running substrates, no per-plugin
  migration mode.
- Kept: DB migrations; run history from the old era stays renderable; a release
  note states plainly that plugin API v1 is removed.
- `plugin-sdk` repositions as: manifest types, signing CLI
  (`gleipnir-plugin keygen|sign|package`, now packaging OCI archives), and
  events-extension server helpers. The runtime scaffolding
  (`serve`, `hostwire`, raw gRPC seams) is deleted.
- The Slack plugin is **rewritten, not migrated** — as the proving ground for
  every profile contract (§4.2 keeps it from re-becoming the template).

## 13. Deferred epic: run resurrection

Scope: rebuild a run's agent context after host restart by replaying `run_steps`,
re-enter the loop, and fire the pending retry/poll — converting `interrupted` from
terminal to recoverable for runs blocked on human waits (and eventually any run).
Until it lands, the durability claim is scoped: persisted `requestState`/task
handles + channel-Request task re-polling survive restarts; runs do not.
Synergy: trace-replay machinery is adjacent to the replay-certification invention
in PATP1586.01 (currently unimplemented).

## 14. WG proposal and contribution boundary

The events extension is implemented first, then proposed to the Triggers & Events
WG with the conformance checklist attached. **Contribution boundary (decided):**

- **Contributed (royalty-free, defensive publication):** the `io.gleipnir/events`
  wire protocol — discover/listen methods, cursor/ack redelivery semantics, the
  host-captured control-plane principle, binding-schema discovery shape.
- **Retained (product):** containment enforcement (network-layer capability
  boundaries, §7), replay certification / run resurrection (§13), the audit
  architecture, policy binding/enforcement machinery.

## 15. Risks

| Risk | Posture |
|---|---|
| Tasks extension maturity (officially graduating; SDK support uneven) | Client-side we control; managed-plugin SDK helpers insulate plugin authors. Watch Go SDK. |
| WG lands a different events standard | Migrate once, from an MCP-shaped extension. Engage early; implementation + conformance suite is our leverage. |
| Third parties adopt `io.gleipnir/events` before the WG concludes | SemVer + deprecation policy from birth (§5). |
| Google schema subset drifts | Translation layer is per-wire and declarative; contract tests per provider (`internal/llm/contract`). |
| Socket-in-CI test cost | Profile conformance checklists anchor a bounded DooD job; in-process stubs remain for unit-level coverage. |
| Version-tag collision: historical phase labels v0.x pre-date tags v1.0.0/v1.1.0 | `v0.2.0-alpha` is the deliberate re-baseline chosen for this effort; release notes must state the ordering explicitly. |

## 16. Deliverables map

ADRs (indexed in the tracker; this document is the body):

| ADR | Title |
|---|---|
| ADR-053 | Plugins are signed, containerized MCP servers; capability-profile model |
| ADR-054 | `io.gleipnir/events` extension — host-captured events, stream-first, WG proposal path |
| ADR-055 | Tool-initiated HITL via MRTR + Tasks — permission/info split, hard caps, TTL answer-replay |
| ADR-056 | Container substrate — socket postures, network-per-instance, egress grants, level-triggered reconciler, OCI-in-Minisign bundles |
| ADR-057 | Host API over per-instance network + instance token; events-vs-host-API boundary rule |
| ADR-058 | User scoping — verified identity linking (Tier 1) + user config schema (Tier 2); Tier 3 deferred |
| ADR-059 | LLM schema policy — lossy presentation, exact enforcement; ADR-017 canonical-schema scoping rule |
| ADR-060 | Hard cutover to `v0.2.0-alpha`; plugin-sdk repositioning; Slack rewrite |

Epic decomposition (created as GitHub milestones; issue decomposition to follow;
sequencing discussion next):

1. MCP client compliance + schema policy — §10, §11 (milestone #13)
2. Tool-initiated HITL routing — §6 (#14)
3. Container substrate + reconciler — §7 (#15)
4. `io.gleipnir/events` extension + WG proposal — §5, §14 (#16)
5. Host API re-plumb — §8 (#17)
6. User scoping Tiers 1–2 — §9 (#18)
7. Slack plugin rewrite (proving ground) — §12 (#19)
8. Profile conformance suite + CI — §4.2 (#20)
9. Cutover, cleanup & docs — §12 (#22)
10. Run resurrection — §13 (standalone, explicitly deferred; #21)

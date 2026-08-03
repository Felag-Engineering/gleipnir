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
| ADR-006 | React frontend, embedded in Go binary              | 🟢 Decided    | v0.1   | Frontend, go:embed, Docker Compose                   |
| ADR-007 | BoundAgent: sensor / actuator / feedback roles     | 🟢 Decided    | v0.1   | Policy schema, runtime, UI                           |
| ADR-008 | Two approval modes (agent-initiated + policy-gated)| 🟢 Decided    | v0.2   | Approval interceptor, feedback channel               |
| ADR-009 | Feedback channel: policy-first, system fallback    | 🟢 Decided    | v0.2   | Policy schema, notification system                   |
| ADR-010 | Project name: Gleipnir                             | 🟢 Decided    | —      | —                                                    |
| ADR-011 | v1 approval path (UI vs Slack callbacks)           | 🟢 Decided    | v0.2   | Approval UX, inbound network model — settled as in-app UI approvals (shipped); the approval lifecycle is governed by ADR-008 + ADR-029 |
| ADR-012 | Run persistence and recovery behavior              | 🟢 Decided    | v0.1   | Run executor, storage layer, startup sequence        |
| ADR-013 | System prompt default template                     | 🟢 Decided    | v0.1   | Agent runtime, policy schema, UI prompt editor       |
| ADR-014 | Poll trigger MCP client architecture               | 🟢 Decided    | v0.3   | Trigger engine, MCP client, package structure        |
| ADR-015 | Policy concurrency model                           | 🟢 Decided    | v1.0   | Trigger engine, run executor, policy schema          |
| ADR-016 | Real-time UI transport: SSE over WebSockets        | 🟢 Decided    | v0.1   | Frontend, Go API, HA scaling path                    |
| ADR-017 | Policy-level parameter scoping for MCP tools       | 🟢 Decided    | v0.1   | Policy schema, MCP client, agent runtime, audit log  |
| ADR-018 | Capability snapshot as first run step              | 🟢 Decided    | v0.1   | Run steps schema, agent runtime, reasoning timeline  |
| ADR-019 | Dual-mode policy editor (form + YAML)             | 🟢 Decided    | v0.1   | Frontend, policy YAML schema                         |
| ADR-020 | Policy folders for UI grouping                    | 🟢 Decided    | v0.1   | Policy YAML schema, frontend dashboard               |
| ADR-021 | MCP discovery diffs                               | 🟢 Decided    | v0.1   | MCP discovery endpoint, frontend                     |
| ADR-022 | ProviderWire seam + cross-wire contract suite      | 🟢 Decided    | v1.1   | internal/llm, all four provider packages, contract suite |
| ADR-023 | Per-policy model selection                         | 🟢 Decided    | v0.1   | Policy schema, agent runtime, capability snapshot    |
| ADR-024 | Webhook HMAC-SHA256 signature verification         | 🟢 Decided    | v0.1   | Webhook handler, policy schema, trigger package      |
| ADR-026 | Model-agnostic design (multi-provider) — revised   | 🟢 Decided    | v1.0   | LLM client interface, agent runtime, policy schema   |
| ADR-028 | Tool risk classification model                     | 🟢 Decided    | v1.0   | Policy schema, runtime approval interceptor          |
| ADR-029 | Approval state machine (v1.0 minimal)              | 🟢 Decided    | v1.0   | BoundAgent runtime, approval handler, SSE, UI        |
| ADR-030 | UI abstracts over tool transport — Tools page is protocol-agnostic | 🟢 Decided | v0.1 | Frontend nav, routes, MCPPage UI text          |
| ADR-031 | Native feedback as a Gleipnir runtime primitive | 🟢 Decided | v1.0 | Agent runtime, policy schema, notify package, UI |
| ADR-032 | Admin-managed OpenAI-compatible LLM provider instances | 🟢 Decided | v1.0 | internal/llm/openaicompat, admin API, admin UI |
| ADR-033 | Premium OpenAI client split from compat client         | 🟢 Decided | v1.0 | internal/llm/openai, internal/llm/openaicompat, main.go |
| ADR-034 | Webhook secrets stored in encrypted DB column (scoped ADR-002 deviation) | 🟢 Decided | v1.0 | policies table, internal/policy, trigger/webhook_handler, frontend WebhookConfig |
| ADR-035 | DB-backed system settings for runtime configuration | 🟢 Decided | v1.0 | system_settings table, admin API, frontend /admin/system |
| ADR-036 | Centralized scheduler dispatcher (NOT IMPLEMENTED — see body) | ⬜ Deferred | v1.0 | (proposed `internal/dispatcher`, never built); live code stays per-loop in internal/trigger/{scheduled,poll,cron}.go |
| ADR-037 | Custom Prometheus registry in internal/infra/metrics (leaf package) | 🟢 Decided | v1.0 | internal/infra/metrics (new), all future instrumented packages |
| ADR-038 | Atomic run-state transitions with optimistic locking   | 🟢 Decided | v1.0 | runs.version column, RunStateMachine.Transition (tx), runstate.ErrTransitionConflict |
| ADR-039 | Per-server encrypted auth headers for authenticated MCP providers | 🟢 Decided | v1.0 | mcp_servers table, internal/mcp, internal/admin, gleipnirctl rotate-key |
| ADR-040 | Arcade gateway pre-authorization (toolkit-level OAuth pre-warm) | 🟢 Decided | v1.0 | internal/arcade (new), internal/http/api/arcade_handler, frontend ServerDetailModal |
| ADR-041 | Plugin system architecture (umbrella) | 🟢 Decided | v1.1 (plugins) | internal/plugin (new), internal/execution/agent/feedback.go, plugin-sdk (new module), admin UI, ADR-004 (parallel to MCP) — cross-source tool uniqueness arbiter lives in `internal/toolregistry` per spec §3.3 / issue #194 |
| ADR-042 | Plugin service & HostAPI versioning policy | 🟢 Decided | v1.1 (plugins) | docs/developer/plugin-system-spec.md §10, buf.yaml, .github/workflows/ci.yml |
| ADR-043 | Plugin signing tooling — bundled Minisign in plugin-sdk/signing, fresh-written | 🟢 Decided | v1.1 (plugins) | plugin-sdk/signing (new), gleipnir-plugin keygen/sign/package subcommands, spec §5.2 §14.5 |
| ADR-044 | Channel routing model — Notify/Request semantics, audience as shared resource | 🟢 Decided | v1.1 (plugins) | internal/plugin/channel (new), internal/execution/agent/feedback.go, admin audiences UI, ADR-031 (partial supersession) |
| ADR-045 | Plugin signing & TOFU trust — Minisign tamper-evidence + first-install pubkey capture | 🟢 Decided | v1.1 (plugins) | internal/plugin (loader), plugin_instances.trusted_pubkey, GLEIPNIR_ALLOW_UNSIGNED_PLUGINS, spec §5 |
| ADR-046 | Audit-table split — run_steps (LLM-visible) vs plugin_audit_events (operator-only) | 🟢 Decided | v1.1 (plugins) | plugin_audit_events table, WriteAuditStep RPC authorization, spec §12.3 |
| ADR-047 | Plugin observability surface — metrics prefix, cardinality cap, Log RPC instead of stdout, OTEL deferred | 🟢 Decided | v1.1 (plugins) | internal/plugin/hostsvc/metrics.go, internal/plugin/hostsvc/handlers.go (Log/EmitMetric), internal/plugin/process/logpipe.go, internal/plugin/state/metrics.go, spec §12 |
| ADR-048 | Subscribed trigger type — internal-only name, flat picker, no JSONPath for plugin bindings, single-trigger-per-policy v1 | 🟢 Decided | v1.1 (plugins) | internal/model (TriggerType), internal/policy (parser/validator), internal/trigger (new subscribed handler), policy editor trigger picker, spec §7 |
| ADR-049 | Redact-on-read for plugin instance config secret fields (x-gleipnir-secret) | 🟢 Decided | v1.1 (plugins) | internal/plugin/configvalidate, internal/admin/plugin_handler, plugin-sdk/manifest, plugins/slack |
| ADR-050 | Ergonomic Service seam coexists with raw gRPC seam in plugin-sdk (amended #495: ergonomic trigger emit routes through canonical EmitEvent, not StartResponse) | 🟢 Decided | v1.1 (plugins) | plugin-sdk/tool, plugin-sdk/channel, plugin-sdk/trigger, plugin-sdk/pluginerr (new packages); plugin-sdk/serve (New*Server constructors, WithXHandler options); plugins/ntfy (migrated); plugins/slack (stays raw) |
| ADR-051 | Three plugin dispatchers are deliberately separate (tool-call pool, channel Notify/Request, trigger events) — do not merge into one routing module | 🟢 Decided | v1.1 (plugins) | internal/plugin/dispatch (pool.go, channel.go), internal/plugin/trigger (dispatcher.go), internal/plugin/hostsvc (identity/generation interceptors) |
| ADR-052 | Operator-selectable binding-match operator per trigger field — field advertises an allowed-operator SET, operator picks via policy-editor dropdown; flat binding = author default op, `{op, value}` = explicit override (back-compat) | 🟢 Decided | v1.x (post-1.1) | internal/plugin/binding, plugin-sdk/manifest, internal/policy, internal/plugin/configvalidate, internal/http/api/binding_test_handler, policy editor trigger form |
| #611    | Remove claudecode agent runtime                        | 🟢 Decided | v1.0 | internal/agent/claudecode deleted; policies using provider: claude-code now fail validation |
| #199    | call_id propagation through gRPC metadata (spec §8.5)  | 🟢 Decided | v1.1 (plugins) | plugin-sdk/serve/callcontext.go, internal/plugin/hostsvc (new package), no new ADR — implements existing spec §8.5 contract |
| #224    | OAuth2 authcode + clientcred host-side orchestration (spec §9.1/§9.2) | 🟢 Decided | v1.1 (plugins) | internal/plugin/oauth (new package, x/oauth2 + clientcredentials), internal/admin/plugin_oauth_handler.go, plugin_instances.credentials_encrypted, HMAC state envelope with HKDF subkey off GLEIPNIR_ENCRYPTION_KEY; no new ADR — implements existing spec §9 contract. Encryption helpers reused from internal/admin via function injection to avoid an import cycle; planned to move to internal/infra/crypto when #141 lands. |
| #226    | Non-OAuth credential strategies: static_api_key, header_set, basic_auth, none (spec §9.1) | 🟢 Decided | v1.1 (plugins) | internal/plugin/oauth.StoredCredentials widened to discriminated union; internal/admin/plugin_credentials_handler.go (write-only API, mirrors ADR-039/034); internal/infra/headervalidate (extracted from internal/mcp to avoid import cycle); plugin-sdk/credentials (typed Apply helper for plugins); no new ADR — implements settled spec §9.1 contract. |
| ADR-053 | Plugins are signed, containerized MCP servers; capability-profile model (tool provider / event source / human channel w/ assurance levels / identity provider) | 🟢 Decided | v0.2.0-alpha | Supersedes ADR-041 substrate + ADR-042 versioning axes; internal/plugin (rework), internal/mcp (unified client), plugin-sdk (repositioned) — body in [mcp-realignment-spec.md](mcp-realignment-spec.md) §3–4 |
| ADR-054 | `io.gleipnir/events` MCP extension — host-captured events (never model context), events/discover + events/listen, at-least-once + cursor, closed-then-extensible, WG proposal path | 🟢 Decided | v0.2.0-alpha | Replaces TriggerService gRPC; internal/plugin/trigger, internal/plugin/dedup (kept), binding pipeline (kept) — body in [mcp-realignment-spec.md](mcp-realignment-spec.md) §5, §14 |
| ADR-055 | Tool-initiated HITL via MRTR + Tasks — permission/info split, distinct from ADR-008 gate, per-run elicitation budget + size caps + rate limit, timeout precedence, TTL answer-replay | 🟢 Decided | v0.2.0-alpha | internal/mcp, internal/execution/agent, audiences, run_steps step types, ADR-031/ADR-044 (Request → durable task; RequestTerminated → tasks/cancel) — body in [mcp-realignment-spec.md](mcp-realignment-spec.md) §6 |
| ADR-056 | Container substrate — DooD via runtime socket (rootless Podman recommended; manual-compose escape hatch), network-per-instance + subnet pool, manifest egress grants, level-triggered reconciler, OCI-image-in-Minisign bundle, per-capability health | 🟢 Decided | v0.2.0-alpha | Replaces internal/plugin/process + generation refcounts; ADR-045 packaging updated (trust model intact) — body in [mcp-realignment-spec.md](mcp-realignment-spec.md) §7 |
| ADR-057 | Host API over per-instance internal network + instance token (not UDS volume); events-vs-host-API boundary rule; +AuthorizeActor/SubmitIdentityProof/GetUserConfig, −WriteAuditStep/EmitEvent | 🟢 Decided | v0.2.0-alpha | internal/plugin/hostsvc, internal/plugin/identity (kept) — body in [mcp-realignment-spec.md](mcp-realignment-spec.md) §8 |
| ADR-058 | User scoping — Tier 1 verified identity linking (plugin_user_identities; dm_code/inbound_code/admin_set), Tier 2 user_config_schema (never grants capability; routing host-interpreted); Tier 3 per-user credentials deferred | 🟢 Decided | v0.2.0-alpha | Replaces users.slack_user_id + GetUserBySlackUserID; user settings UI; audience dispatcher — body in [mcp-realignment-spec.md](mcp-realignment-spec.md) §9 |
| ADR-059 | LLM schema policy — discovery does BYTE-LEVEL NORMALIZATION ONLY (decode + sort keys + re-emit, no $ref resolution, no allOf flattening — three attempts at doing either safely each found a new widening mechanism, see #735); $ref resolution AND allOf flattening both moved into per-provider lossy translation w/ UI notice; exact enforcement against the stored (byte-normalized, otherwise untransformed) schema (validator resolves $ref/handles allOf natively); ADR-017 scoping enforces the policy narrowing as an INDEPENDENT compile alongside the stored schema (composing it as a single additional allOf branch is NOT a narrowing by construction — it widens when the stored schema carries unevaluatedProperties/unevaluatedItems, since the appended branch's own properties/items annotation can satisfy another branch's unevaluated* check; fail closed at save instead when independent compiles aren't used), unions branch property names for policy-editor DISPLAY only, rejected at save when governance can't be determined | 🟢 Decided | v0.2.0-alpha | internal/llm (all wires), internal/mcp discovery, internal/policy validator, internal/schemanorm (#735) — body in [mcp-realignment-spec.md](mcp-realignment-spec.md) §10 |
| ADR-060 | Hard cutover: gRPC plugin substrate removed outright in v0.2.0-alpha (no dual-running; no known third-party plugins); plugin-sdk → manifest types + signing CLI + events helpers; Slack plugin rewritten as proving ground | 🟢 Decided | v0.2.0-alpha | plugin-sdk, plugins/slack, DB migrations, release notes — body in [mcp-realignment-spec.md](mcp-realignment-spec.md) §12 |

**Index notes.**
- **ADR-025 and ADR-027 were never assigned** — the numbering intentionally skips them; there is no missing record.
- **ADR-011, ADR-012, ADR-013, ADR-014, and ADR-043 are index-only** (no dedicated `## ADR-NNN` body below). ADR-011's approval lifecycle is documented under ADR-008 + ADR-029; ADR-043's signing tooling is documented inside the ADR-045 body.
- **ADR-036 was deferred and never implemented** — see its body for the per-loop trigger architecture that shipped instead.
- **ADR-053 through ADR-060 share a single body**: [mcp-realignment-spec.md](mcp-realignment-spec.md) (MCP 2026 realignment, target tag `v0.2.0-alpha`). Decided 2026-07-30; the §10/§11 MCP-client-compliance and schema-policy half has shipped (#733–#748, #762 — the client probes and pins a per-server protocol revision and speaks the 2026-07-28 request shaping to pinned-modern servers; one security-labelled residual remains in the schema-policy half — #769: ADR-017 `params` does not narrow branch-keyword `oneOf`/`anyOf` schemas), while the plugin/container/HITL half is not started. Operator-facing migration notes for the shipped client half: [docs/user/mcp-protocol-migration.md](../user/mcp-protocol-migration.md). Note the phase label `v0.2.0-alpha` is a deliberate version re-baseline that post-dates the shipped `v1.0.0`/`v1.1.0` tags.

---

## ADR-052: Operator-selectable binding-match operator per trigger field

**Status:** Decided
**Date:** 2026-06

### Context

A plugin subscribed-trigger binding (spec §7, ADR-048) is a flat map of `field → value` pairs in the policy YAML, evaluated at runtime by `internal/plugin/binding` against every event payload the plugin emits. Today the *match operator* for each field is fixed by the plugin author: the host resolves it purely from the field's JSON Schema shape (`internal/plugin/binding/binding.go`, `compileField`):

```
field name "mention_only" + type boolean → OpMentionOnly
type string, format:regex                → OpRegex
type string, format:contains             → OpContains
type string (no format)                  → OpEquals
type boolean / number / integer          → OpEquals
```

The author chooses the operator by typing the manifest struct field as `manifest.RegexField`, `manifest.ContainsField`, or `manifest.EqualsField` (`plugin-sdk/manifest/filters.go`), each of which reflects to a `{type, format}` shape. The operator therefore lives in the manifest and cannot be changed by the operator who authors a policy — they may only fill in the *value*.

This surfaced through Slack keyword routing: the `text` field is wired to case-sensitive `contains` and an operator cannot opt into `^(?i)recipe:` without a plugin change. The author-fixed sibling-field workaround shipped as #603 (a `text_regex` `RegexField` peer of `text`) and #604 (a `channel_type` `EqualsField`). Those are concrete consequences of the broader boundary question raised by #607: *which parts of trigger matching belong to the plugin author (fixed in the manifest) vs. the operator (configured per policy)?*

### Decision

**The binding-match operator becomes operator-selectable per field.** A field advertises a SET of allowed operators in its `binding_schema`; the policy editor renders a matcher dropdown (e.g. contains / regex / equals) and the operator picks one per policy. This is the chosen v1.x direction — not the author-fixed status quo, and not a hybrid.

Author guidance and the broader author-fixed-vs-operator-configurable boundary (issue #607 AC) are recorded in the *Boundary guidance* section below.

#### 1. Back-compatible dual wire format

`model.TriggerConfig.Binding` stays `map[string]any`. A field value may take one of two shapes:

- **Flat scalar — `field: value`** — means "match using this field's author-declared *default* operator". This is the existing form; every existing flat binding keeps working unchanged.
- **Explicit object — `field: {op: <operator>, value: <value>}`** — the operator's per-policy override. `op` selects from the field's advertised allowed-operator set; `value` is the match value.

The two are distinguishable at runtime without a schema migration: a binding value that is a `map[string]any` carrying an `op` key is the explicit form; any scalar (string/bool/number) is the flat form. Precedence: when the explicit `op` is present it wins; otherwise the field's default op (from the schema) is used. The `{op, value}` object lives entirely inside the `binding` map in the policy YAML — no new DB columns, consistent with ADR-002 (policy stored as a YAML blob).

#### 2. Manifest representation: `MultiOpField`

A plugin author declares an allowed-operator set with a new SDK typed field, `manifest.MultiOpField`, alongside the existing `RegexField`/`ContainsField`/`EqualsField`/`GlobField` in `plugin-sdk/manifest/filters.go`. Its `JSONSchema()` emits an `x-` extension carrying the allowed set and a default:

```yaml
text:
  type: string
  x-gleipnir-operators: [contains, regex, equals]
  x-gleipnir-default-op: contains
```

The `x-gleipnir-` extension prefix is chosen for consistency with ADR-049's `x-gleipnir-secret` and the established SDK reflection pattern, rather than overloading JSON Schema `format` (which implies validation, whereas this is matcher metadata). The existing single-operator typed fields (`RegexField`, `ContainsField`, `EqualsField`) remain the way to declare a field with exactly one fixed operator; conceptually each is the degenerate `x-gleipnir-operators: [<that op>]` with that op as the default. The host resolves a field's *default op* from its `x-gleipnir-default-op` when present, else from the legacy `{type, format}` shape — so author-fixed fields and operator-selectable fields coexist in one schema.

#### 3. Relationship to #603/#604 (sibling fields)

The author-fixed sibling fields that just shipped — `text` (`ContainsField`), `text_regex` (`RegexField`), `channel_type` (`EqualsField`), `user` (`EqualsField`), and the reserved `mention_only` — **remain valid and keep working**. Each is a field whose *default op* is its current schema-shape operator. Operator-selectability is purely additive: it lets a single field advertise multiple operators (e.g. `text` offering `[contains, regex, equals]`) so an author no longer *needs* to add a `text_regex` peer to expose regex.

The sibling-field pattern is therefore a v1 stepping stone, **kept for back-compat, not subsumed and not removed**. There is no forced migration: existing policies with flat `text:`, `text_regex:`, `channel_type:`, `user:`, and `mention_only:` bindings continue to validate and evaluate identically. New authors are guided toward a single `MultiOpField` instead of sibling fields. This deliberately avoids churning code that just landed.

#### 4. mention_only stays a fixed-semantic reserved field

`mention_only` is **not** operator-selectable. It remains the reserved boolean field whose `true` value maps to `OpMentionOnly` (checking the payload's `mentioned` field, per spec §7.2). It is a fixed-semantic flag, not a value-matched field, so the operator dropdown does not apply to it. This keeps the model with one clean, documented exception rather than trying to force a boolean flag through the operator-set machinery.

### Boundary guidance (issue #607 AC)

Four places can carry trigger-matching configuration. The intended division:

| Layer | Owner | Granularity | What belongs here |
|-------|-------|-------------|-------------------|
| **Manifest schema shape** (`binding_schema` fields, allowed-operator set) | Plugin author | Structural | *Which* fields exist and *which operators* are permitted on each. Author-fixed structure. |
| **Subscription scope** (`subscription_schema`, instance `subscription_scope_json`) | Admin (per instance) | Coarse | Instance-wide watch filter applied before per-policy matching (e.g. Slack channel IDs the instance subscribes to). One setting shared by all policies on that instance. |
| **Per-policy binding** (`binding` map in policy YAML) | Operator (per policy) | Fine | The actual match values, and now the *chosen operator* per field. Per-policy. |
| **Instance config** (`config_json`, ADR-049) | Admin (per instance) | Instance-wide | Credentials and operational tunables (e.g. rate limits, #577) that are not matching criteria. |

Author guidance — when to expose a binding field vs subscription scope vs instance config:

- **Binding field** — a per-policy matching criterion an operator should vary per policy (message text, sender). Expose an allowed-operator set when more than one match style is reasonable.
- **Subscription scope** — a coarse instance-wide filter that bounds *what the plugin watches at all* and is shared by every policy (Slack channels the instance connects to). Prefer scope over binding when the filter reduces the plugin's external footprint, not just which events fire a run.
- **Instance config** — credentials and operational knobs that are not matching criteria and that an admin (not a policy operator) should control.

The principle: **the plugin author fixes the structure (fields and allowed operators); the operator picks values and operator per policy; the admin sets coarse instance scope and operational config.**

### Consequences

- **Binding engine** (`internal/plugin/binding`): `Compile` must parse the optional `{op, value}` object form, resolve `Op` from the explicit `op` when present (validating it against the field's advertised set) and otherwise from the field's default op. The leaf-package boundary (stdlib + yaml.v3 only) is preserved.
- **SDK** (`plugin-sdk/manifest`): add `MultiOpField` and the `x-gleipnir-operators` / `x-gleipnir-default-op` reflection.
- **Save-time validation** (`internal/plugin/configvalidate.ForTriggerBinding`, `internal/policy/subscribed_validator.go`): must accept the object form and reject an `op` not in the field's advertised set.
- **Preview endpoint** (`internal/http/api/binding_test_handler.go`): already calls `binding.Compile`/`Evaluate`, so it inherits the new form once the engine changes; its request body shape (flat `binding` map) is unchanged because the object form is just a richer value.
- **Policy editor**: the trigger form (ADR-019/048 authoring surface) must render a matcher dropdown per multi-op field and emit the `{op, value}` shape; single-op fields keep their current single input.
- **Docs**: `docs/developer/plugin-system-spec.md §7` and the plugin author guide gain the operator-set field and the boundary guidance above.

No DB migration is required: the change is confined to the YAML-blob binding payload (ADR-002) and the reflected manifest schema.

### Follow-up issues

This ADR records the decision; implementation is split into:

1. Binding engine — parse the explicit `{op, value}` form; resolve `Op` from explicit `op` (else default op); validate `op` against the advertised set (`internal/plugin/binding`).
2. SDK manifest — `manifest.MultiOpField` + `x-gleipnir-operators` / `x-gleipnir-default-op` reflection (`plugin-sdk/manifest/filters.go`).
3. configvalidate / save-time validator — accept the object form, reject out-of-set operators (`internal/plugin/configvalidate`, `internal/policy/subscribed_validator.go`).
4. test-binding preview — confirm the preview endpoint handles the object form end-to-end (`internal/http/api/binding_test_handler.go`).
5. Policy-editor matcher dropdown UI (frontend trigger form).
6. Docs — spec §7 + plugin author guide (boundary guidance, `MultiOpField`).

Concrete examples / cross-links: #603 (`text_regex` sibling — becomes unnecessary once `text` is a `MultiOpField`) and #604 (`channel_type`) are the author-fixed precedents this ADR generalizes. Source design question: #607.

### References

- Issue #607 (design question + acceptance criteria), #603, #604, #577
- `internal/plugin/binding/binding.go` (`compileField`, operator table)
- `plugin-sdk/manifest/filters.go` (`RegexField`/`ContainsField`/`EqualsField`/`GlobField`)
- `internal/plugin/configvalidate/configvalidate.go` (`ForTriggerBinding`), `internal/policy/subscribed_validator.go`
- `internal/http/api/binding_test_handler.go`
- ADR-002 (policy as YAML blob), ADR-019 (policy editor authoring surface), ADR-048 (subscribed trigger type), ADR-049 (`x-gleipnir-secret` extension precedent)

---

## ADR-051: Three plugin dispatchers are deliberately separate — do not merge

**Status:** Decided
**Date:** 2026-06

### Context

Issue #508 was originally proposed as a "consolidate three dispatchers" refactor, based on the observation that `internal/plugin/dispatch/pool.go`, `internal/plugin/dispatch/channel.go`, and `internal/plugin/trigger/dispatcher.go` all route plugin-related work. After investigation, the consolidation was rejected and the issue was re-scoped to two standalone deliverables: e2e safety-net tests (#508) and this ADR. This ADR records the deliberate design intent so future architecture-review passes do not re-propose the incorrect consolidation.

### Decision

The three dispatchers are **intentionally separate**. Their surface similarity (all route something to a plugin) obscures fundamental structural differences that make consolidation wrong.

#### 1. Distinct routing keys

| Dispatcher | Routing key | Location |
|------------|-------------|----------|
| `dispatch/pool.go` (tool calls) | Stable plugin **instance name** (via `<instance>.<tool>` policy capability grant) | `internal/plugin/dispatch/pool.go` |
| `dispatch/channel.go` (Notify/Request) | **Audience → instance** mapping (admin-managed ordered list in `audiences`/`audience_entries` tables) | `internal/plugin/dispatch/channel.go` |
| `trigger/dispatcher.go` (events) | Plugin **instance ID** (from the supervisor's `TriggerService.Start` stream) | `internal/plugin/trigger/dispatcher.go` |

These are three different routing namespaces. A unified dispatcher would need to accept all three key types, making every dispatch path pay the overhead of distinguishing them at runtime — adding complexity with no benefit.

#### 2. Distinct concurrency models

| Dispatcher | Concurrency model |
|------------|-------------------|
| `dispatch/pool.go` | Per-instance semaphore + bounded queue gate (spec §13.2/§13.6); in-flight call cancellation via `Cancel(call_id)` with 5s deadline + force-disconnect fallback (spec §13.8, issue #198). Calls are long-lived and may need individual cancellation. |
| `dispatch/channel.go` | Synchronous waiter: `Notify` is parallel fan-out with a 10s deadline; `Request` routes to exactly one entry and waits for the async `WriteAuditStep` callback. No per-call cancellation — individual entry timeouts govern. |
| `trigger/dispatcher.go` | Synchronous launch: `Handle` evaluates all subscribed policies and calls `RunLauncher.Launch` for matches. No in-flight tracking — fire-and-forget into the run manager. |

Merging these into a single module would require supporting all three models simultaneously, with no shared code path because the models are orthogonal.

#### 3. Distinct persistence

| Dispatcher | Persistence |
|------------|-------------|
| `dispatch/pool.go` | None — in-memory call tracking only; runs are persisted by the agent runtime, not the dispatcher. |
| `dispatch/channel.go` | Writes `plugin_pending_requests` rows for `Request` routing; the callback path (`WriteAuditStep`) resolves them. |
| `trigger/dispatcher.go` | None — event dedup is handled by `internal/plugin/dedup` (a separate leaf package); run launch is handled by `RunLauncher`. |

#### 4. Identity/generation gating lives in the hostsvc interceptor layer — not in any dispatcher

All three dispatchers are **host-to-plugin** paths (the host dispatches work to the plugin). The security gate — identity-token verification and per-generation refcounting — lives in the **plugin-to-host** path, specifically in the gRPC unary interceptor chain on the hostsvc gRPC server:

```
plugin → network → UnaryInstanceTokenInterceptor → UnaryGenerationRefcountInterceptor → handler
```

Merging the three dispatchers into one routing module would not consolidate this gate, because the gate is on the opposite side of the wire. The interceptors live in `internal/plugin/hostsvc` and apply to **every** host RPC regardless of which dispatcher initiated the corresponding outbound call. They are correctly placed and should not be moved into dispatcher code.

The e2e safety-net tests added in issue #508 (`internal/plugin/e2e/identity_rotation_e2e_test.go` and `internal/plugin/e2e/generation_drain_e2e_test.go`) lock this gating behavior over a real gRPC wire with the production interceptor chain, serving as a regression guard across Phase 5 work.

### Decision: do not merge

The original #508 "consolidate three dispatchers" proposal is **explicitly rejected**. The three dispatchers are structurally separate by design and should remain so. Adding a shared routing module would obscure the distinct semantics of each dispatch path, increase coupling between three independently-understandable components, and provide no runtime or maintenance benefit.

### References

- Issue #508 (re-scoped; original consolidation proposal and investigation comment)
- `internal/plugin/dispatch/pool.go`, `internal/plugin/dispatch/channel.go`
- `internal/plugin/trigger/dispatcher.go`
- `internal/plugin/hostsvc/identity.go` (`UnaryInstanceTokenInterceptor`)
- `internal/plugin/hostsvc/generation_interceptor.go` (`UnaryGenerationRefcountInterceptor`)
- `internal/plugin/e2e/identity_rotation_e2e_test.go`, `internal/plugin/e2e/generation_drain_e2e_test.go`
- ADR-041 (plugin system architecture), ADR-044 (channel routing model)

---

## ADR-049: Redact-on-read for plugin instance config secret fields

**Status:** Decided
**Date:** 2026-05

### Context

Per-instance plugin config (`config_json`, validated against the manifest's `ConfigSchema`) is encrypted at rest, but the admin GET endpoint (`GET /api/v1/admin/plugins/{id}/instances/{iid}`) previously returned the decrypted JSON verbatim. Any admin with access to that endpoint could read secrets stored in config fields.

This generalizes the write-only patterns established by ADR-034 (webhook secrets) and ADR-039 (MCP server auth headers) to the per-instance plugin config blob. The immediate trigger is the Slack plugin's `app_level_token` (xapp- prefix) added in PR #367, which would otherwise be readable on GET.

### Decision

#### 1. Annotation: `x-gleipnir-secret: true`

Properties holding secrets are annotated with the JSON Schema extension key `x-gleipnir-secret: true`. This was chosen over `format: gleipnir-secret` for three reasons:
- JSON Schema `format` semantics imply validation; secret is metadata, not a validator.
- `x-` extension keys are the canonical JSON Schema extension mechanism.
- It composes cleanly with type-specific `format` values (e.g. `"uri"`).

#### 2. SDK marker: `SecretString` typed string

Go plugin authors declare secrets by typing config struct fields as `manifest.SecretString`. The `JSONSchema()` method returns `{type: string, extras: {"x-gleipnir-secret": true}}`, following the same pattern as `RegexField`, `ContainsField`, and `GlobField` in `plugin-sdk/manifest/filters.go`. Hand-authored YAML manifests add `x-gleipnir-secret: true` directly.

#### 3. Redaction is read-time only

Storage shape is unchanged; the `config_json` column continues to hold the raw plaintext (which is already encrypted at rest by the column-level protections). Redaction happens in the Go handler before serializing the HTTP response. The redaction sentinel is the string `"***"`.

#### 4. Per-field write-only PUT endpoint

A new `PUT /api/v1/admin/plugins/{id}/instances/{iid}/config/{property}` endpoint mirrors the ADR-039 `PUT /mcp/servers/:id/headers/:name` pattern: one property at a time, CAS-guarded by `expected_version`. This lets the UI update a single secret field without transmitting all config properties in the request body.

#### 5. Bulk PUT rejects the redaction sentinel

The existing `PUT /api/v1/admin/plugins/{id}/instances/{iid}/config` continues to work but rejects requests that include `"***"` as the value for any secret field. This prevents the round-trip clobber: UI reads `"***"`, user hits Save, real secret is overwritten with the sentinel.

#### 6. GET returns 500 on manifest-parse failure (fail-closed)

If the manifest cannot be parsed when serving a GET, the handler returns 500 ("corrupt manifest snapshot") rather than falling through to an unredacted response. This matches the ADR-001 posture: fail closed, never silently omit a security control.

#### 7. Fallback synthesized responses also redact

Both write handlers (`PutInstanceConfig` and `PutInstanceConfigProperty`) synthesize a fallback response when the post-write re-fetch fails. The fallback path applies the same redaction so neither code path can emit raw secret JSON.

### Deferred

- Per-user / per-policy config scoping (v2 `user_credentials` mode, spec §17).
- Migrating `StoredCredentials.Token` to this mechanism (it is already write-only via the OAuth callback).
- Nested object secrets (v1 redacts only top-level `properties`).
- Frontend UI surface (tracked as follow-up after this API change).

---

## ADR-050: Ergonomic Service seam coexists with raw gRPC seam in plugin-sdk

**Status:** Decided
**Date:** 2026-05

### Context

The only author-facing seam for implementing plugin behaviour was the raw generated gRPC server interface injected via a host-client factory (e.g. `serve.WithToolService(func(host hostv1.HostServiceClient) toolv1.ToolServiceServer { ... })`). Authors had to implement `toolv1.ToolServiceServer` directly: deal in `*toolv1.CallRequest` / `*toolv1.CallResponse`, marshal/unmarshal `input_json` / `output_json` by hand, and construct `commonv1.ErrorEnvelope` values on every error path. The `plugin-sdk/examples/minimal-tool` service.go showed the cost: ~90 lines of proto plumbing for a trivial echo implementation.

The `gleipnir-plugin new` scaffold's `service.go` template was already written to an ergonomic interface (`Call(ctx, name string, input []byte) ([]byte, error)`) that no SDK adapter satisfied, leaving the scaffold uncompilable. This gap motivated issue #457.

### Decision

#### 1. Proto-free ergonomic interfaces in new sub-packages

Three new sub-packages — `plugin-sdk/tool`, `plugin-sdk/channel`, `plugin-sdk/trigger` — define ergonomic service interfaces (`tool.Service`, `channel.Service`, `trigger.Service`) with no proto types in their signatures. A fourth sub-package `plugin-sdk/pluginerr` provides a proto-free error-code enum and constructors (`InvalidArg`, `NotFound`, `Internal`, `Unavailable`, `Permission`, `RateLimited`, `Unimplemented`) so authors can signal structured errors without importing `gen/`.

These packages are intentional leaf packages: they must not import `plugin-sdk/gen/` (the proto coupling lives only in `serve/`).

#### 2. Adapters in serve/ via exported constructors

`plugin-sdk/serve` receives three exported adapter constructors — `NewToolServer(tool.Service)`, `NewChannelServer(channel.Service)`, `NewTriggerServer(trigger.Service)` — that bridge the ergonomic interfaces onto the generated gRPC server interfaces. Error mapping is centralized in `serve/erroradapt.go`: `pluginerr.CodedError` maps 1:1 onto `commonv1.ErrorCode`; plain errors map to `ERROR_CODE_INTERNAL`. This is the only place in the codebase where proto error codes and ergonomic codes are coupled.

`ListToolsResponse` has no `Error` field, so `ListTools` errors become a gRPC-level `codes.Internal` status (not an envelope). All other service method errors become application-level envelopes.

The adapter structs stay unexported; the constructors are public. This lets tests register the real adapter for live gRPC round-trip coverage without exposing struct internals.

#### 3. New ergonomic options alongside the raw options (last-option-wins)

Three new option functions — `WithToolHandler`, `WithChannelHandler`, `WithTriggerHandler` — accept `func(hostv1.HostServiceClient) X.Service` factories and wrap the returned value via the exported constructors before storing it in the same `config.{tool,channel,trigger}Factory` field as the raw `WithXService` options. Zero changes to `server.go` are required: wiring, capability derivation (`Negotiate` keys off `*Factory != nil`), and the `Bind` install path are identical.

Both raw and ergonomic options write the same config field. Last applied wins (newConfig applies options in order). Passing both `WithToolService` and `WithToolHandler` is valid but the earlier one is silently dropped; the doc comments warn against it.

#### 4. Dogfood validation: ntfy migrated, slack stays raw

`plugins/ntfy` was migrated to the ergonomic seam in the same PR (issue #457) as proof that the interface satisfies a real second consumer. `ChannelService` now implements `channel.Service`; `Request` returns `pluginerr.Unimplemented` (Notify-only plugin); `main.go` uses `WithChannelHandler`.

`plugins/slack` intentionally stays on the raw `WithChannelService` seam. Slack's implementation exercises the full proto surface (direct `RequestRequest` field access, custom `NotifyResponse` construction) and the raw seam remains the right choice there. Slack's migration is tracked as a separate non-blocking follow-up.

The acceptance criterion "raw variant kept as a second example if still useful" is satisfied by explicit decision: `plugins/slack` is the canonical raw consumer; no second raw example is added to `examples/`.

#### 5. Host-client injection via factory, not context accessor

Host-client injection uses the existing `func(hostv1.HostServiceClient) X.Service` factory pattern (same as the raw seam). The adapter does not auto-apply `serve.WithCallContext` — authors call it themselves inside service method bodies before outbound host RPCs. This preserves correct behaviour in detached goroutines (e.g. Trigger.Start background workers) where the adapter cannot know the correct propagation scope.

### Amendment (2026-06, issue #495): ergonomic trigger emit routes through EmitEvent

ADR-050 §2 originally adapted `trigger.Service`'s `emit(trigger.Event)` callback onto
`stream.Send(StartResponse)`. This contradicted the original blessed trigger design (#214)
and spec §4.3, which name the `EmitEvent` Host RPC as the single canonical event-delivery
mechanism — and which the reference `plugins/slack` already used, explicitly stating the
Start stream carries no `StartResponse` messages. A third-party author following the SDK
docs built a `stream.Send` delivery path that the reference impl said was unused.

**Resolution:** the ergonomic seam is preserved unchanged at the author surface — authors
still implement `trigger.Service.Start(ctx, scope, emit)` in plain Go types. Internally,
`serve.NewTriggerServer` now takes the bound `hostv1.HostServiceClient` and the `emit`
callback routes each event through `host.EmitEvent` (not `stream.Send`). The
`TriggerService.Start` response stream is held open by the host purely as a
liveness/cancellation channel and carries no events. This satisfies BOTH #214 (EmitEvent
canonical) and ADR-050 (keep the ergonomic API): authors get the nice `emit` signature,
and their events flow through the path that carries identity, per-instance rate limiting,
the payload size cap, SSE observability, and generation-drain semantics.

The host supervisor's `recvLoop` still drains the stream and would dispatch a stray
`StartResponse` defensively (dedup catches duplicates), but `StartResponse` as a delivery
path is deprecated, undocumented, and produced by neither the SDK nor any reference plugin.
Removing it from the proto is a breaking wire change deferred under ADR-042's deprecation
window. SDK docs (`serve/doc.go`), the ergonomic seam (`serve/handleradapters.go`), and the
Slack reference (`plugins/slack/service.go`) now tell one consistent story.

### Deferred

- Migration of `plugins/slack` to the ergonomic seam (tracked follow-up; not required to land this change).
- Wrapper support for `FeedbackRequest.ChannelConfig` field-level helpers (v2, when feedback channel plugins are more common).
- Proto-level removal of `TriggerService.StartResponse` as a delivery mechanism (breaking wire change; ADR-042 two-major deprecation window). See issue #495 amendment.

### References

- Spec §14.1 (SDK), §14.3 (manifest authoring), §14.6 (new scaffold), §4.3 (trigger event delivery)
- Issue #457; Issue #495 (event-delivery unification amendment); Issue #214 (original blessed EmitEvent design)

---

## ADR-048: Subscribed trigger type

**Status:** Decided
**Date:** 2026-05

### Context

The plugin system (ADR-041) introduces event-emitting plugin triggers via `TriggerService.Start` (spec §4.3 / §7). Today there are five built-in trigger types (`webhook`, `manual`, `scheduled`, `poll`, `cron`); a sixth internal type is needed to bind a policy to a `(plugin_instance, event_kind)` pair declared in an installed plugin's manifest.

This ADR records the four settled decisions that define how plugin-sourced triggers integrate with the existing policy model. The primary spec reference is `docs/developer/plugin-system-spec.md` §7. This ADR refines (does not supersede) `docs/developer/adding-a-trigger-type.md`.

### Decision

#### 1. Internal-only trigger type name; flat conceptual picker

The new trigger type is named `subscribed` for storage and dispatch purposes. It is used as the DB CHECK constraint value on `policies.trigger_type`, `runs.trigger_type`, and `trigger_queue.trigger_type`, and as the discriminator in handler switch-cases. It is never shown to operators.

From spec §7.1: "**The trigger picker UI is flat at the conceptual level** — operators see built-ins (`webhook`, `manual`, `scheduled`, `poll`, `cron`) and every plugin event_kind as peer options. … The internal `subscribed` type is never rendered as a label."

The user-facing model is a `(source, event_kind)` pair. The picker presents plugin event_kinds alongside the five built-ins as peers. At scale, entries are grouped visually by source (built-ins as one top-of-list group; each plugin instance as its own group), with a search box across all entries — but the grouping is purely presentational. The underlying selection is still a single `(source, event_kind)` pair stored as `trigger_type: subscribed` in the policy YAML.

#### 2. Multi-instance disambiguation in the picker

Multiple instances of the same plugin contribute separate, disambiguated entries in the trigger picker. The spec example (§7.1): `Slack (slack-prod): Channel message` vs `Slack (slack-personal): Channel message`. Both map to the same internal `subscribed` type but carry different `source:` values in the policy YAML binding (§7.2).

Instances are credential/configuration envelopes — two instances of the same plugin may watch entirely disjoint workspaces, channels, or tenants. Combining them under a single picker entry would require the operator to then disambiguate in a second step; listing them separately makes the `(source, event_kind)` pair the first-class selection unit. The YAML representation is:

```yaml
trigger:
  type: subscribed              # internal, never rendered in UI
  source: slack-prod            # plugin instance
  event_kind: channel_message
  binding:
    channel: "#incidents"
    mention_only: true
```

#### 3. No JSONPath for plugin trigger bindings — rich field types instead

Plugin trigger bindings use typed form fields (regex, contains, equals, etc.) derived from the manifest's `event_kinds[].binding_schema`. JSONPath is explicitly excluded from plugin trigger filtering.

From spec §7.2: "Plugin authors express filters via richer fields (regex, contains, equals, etc.) — **JSONPath is not used for plugin trigger filtering.**" (JSONPath remains in the built-in `poll` trigger, which evaluates MCP tool output.)

Rationale:

- **JSONPath is stringly-typed.** It provides no autocomplete, no schema validation, and no UI affordance. An operator who mistyped a path gets a silent non-match, not a validation error.
- **Typed binding schema enables a structured form.** The manifest's `binding_schema` is a JSON Schema document (reflection-derived from Go struct types). The policy editor renders it as a typed form per `event_kind` — the same mechanism used for instance config in §4.2.
- **Filters run host-side against typed Go fields (§7.3).** There is no per-match round-trip RPC to the plugin; the host evaluates binding predicates directly against the event payload. This means the host, not the plugin, owns the evaluation semantics — and the schema defines those semantics at install time.
- **Filter expressiveness extends additively.** New operator types (prefix, suffix, numeric range) widen the binding schema without changing the trigger type or the YAML shape. JSONPath cannot be extended this way without versioning the expression language.
- **Explicit asymmetry with `poll`.** The `poll` trigger evaluates arbitrary MCP tool output — no per-tool typed schema is available, so JSONPath is the only tractable option. Plugin triggers have a manifest-declared schema; the typed approach is strictly better in that context. The asymmetry is intentional.

#### 4. v1 single-trigger-per-policy limit; structured-form path for future generalization

From spec §7.4: "One trigger binding per policy. Multiple triggers per policy is deferred. The structured-form approach generalizes cleanly when needed."

Today's policy YAML `trigger:` key is a single object across all five built-in trigger types. Widening it to a list before there is a real user requirement would compound parser, validator, and UI complexity with no concrete gain.

Forward path: when multi-trigger per policy becomes a real requirement, `trigger:` becomes a list of `(source, event_kind, binding)` entries. Each entry is still rendered by the same per-`event_kind` form component — a `map(renderForm)` call over the list rather than a new abstraction. No proto-level changes and no new DB CHECK constraint values are forced at that point; `subscribed` continues to cover all plugin-sourced trigger entries regardless of how many appear in the list.

### Out of scope

- Multi-trigger per policy (deferred per §7.4).
- Per-event-type routing overrides on the audience side (§6.4).
- Paste-your-own-JSON binding tester (§7.5; v1 surface is "Test against sample" using manifest `examples`).
- Operator-side JSONPath in plugin bindings (rejected, see decision §3).
- Implementation of the `subscribed` handler, parser, validator, migration, and frontend picker — tracked separately under parent #160.

### Consequences

- `TriggerType` enum (`internal/model`) and DB CHECK constraints on `policies.trigger_type`, `runs.trigger_type`, and `trigger_queue.trigger_type` must accept `subscribed` (steps 1 and 6 of `adding-a-trigger-type.md`). This is one-time work when `subscribed` ships; subsequent plugin event_kinds are additive at the manifest level only.
- The policy-editor trigger picker grows from a fixed list of five built-ins to a dynamic list assembled from "five built-ins + (plugin instances × declared `event_kinds`)". The internal storage discriminator stays small (six enum values).
- Plugin authors cannot use JSONPath expressions in their manifest's `binding_schema` — bindings are typed Go-struct fields surfaced as a structured form. Plugin authors get a schema validation error at manifest install time if they attempt one.
- When multi-trigger per policy becomes a real requirement, the schema migration is YAML-shape only (single object → list of objects). No new trigger type enum value and no new handler dispatch path is required.

---

## ADR-047: Plugin observability surface

**Status:** Decided
**Date:** 2026-05

### Context

The plugin system (ADR-041) introduces subprocess processes that emit logs and metrics back to the host. Without a defined observability surface, each plugin author would invent their own naming scheme, make independent cardinality decisions, and choose between stdout and gRPC for log delivery — creating an inconsistent operator experience and, in the worst case, unbounded Prometheus cardinality that crashes the host registry.

This ADR records the observability decisions encoded in Phase 4 (issues #200 and #287). It is a strict predecessor of issue #205 (dispatcher metrics), so new metric names in the dispatcher can be reviewed against a written standard rather than archaeology in handler code.

The primary spec reference is `docs/developer/plugin-system-spec.md` §12.

### Decision

#### 1. `gleipnir_plugin_*` metric namespace — host force-prefixes

All plugin-emitted metrics carry the `gleipnir_plugin_` prefix. Plugins submit bare names via `EmitMetric`; the host prepends the prefix unconditionally before registration (`internal/plugin/hostsvc/metrics.go:81`: `fullName := "gleipnir_plugin_" + name`). Plugins that try to pre-apply the prefix themselves receive an `invalid_metric_name` error — the host detects and rejects names that already start with `gleipnir_plugin_` (`metrics.go:68-70`).

This design means plugins cannot escape the namespace (e.g. emit a metric named `gleipnir_core_something` to impersonate a host metric), and operators see a consistent `gleipnir_plugin_` prefix for everything plugin-sourced when browsing `/metrics`.

#### 2. Auto-injected `plugin` and `instance` labels

The host appends `plugin` (the stable plugin ID) and `instance` (the instance ID) to every `GaugeVec` registration and every `With()` call for plugin-emitted metrics (`metrics.go:113-117`, `metrics.go:162-169`). Plugins do not supply these labels; submitting them explicitly via the RPC is an error (`reserved_label`; `metrics.go:75-79`).

Auto-injection ensures that multi-instance deployments of the same plugin are always separable by label without any coordination from the plugin author. The label pair is the same `plugin`/`instance` pair used in log attrs emitted by the `Log` handler (`handlers.go:366-367`).

#### 3. Cardinality cap — 100 distinct values per (metric, label-key), loud rejection

The host tracks the set of distinct values per `(metric, label-key)` pair. Once the set reaches 100 values, any subsequent `EmitMetric` call that would add a new value is rejected with `codes.ResourceExhausted` and the error code `cardinality_cap_exceeded` (`metrics.go:14-16`, `metrics.go:94-100`, `handlers.go:298-303`). The call is rejected before any Prometheus registration occurs.

The rejection is **loud** (gRPC error returned to the plugin, not silently swallowed) by deliberate design:

- Silent drops hide misconfigured high-cardinality labels during development. A plugin author whose metric silently stops updating has no signal that the cap was reached.
- Loud rejection surfaces in integration tests and local dev runs immediately. The plugin author can observe the `ResourceExhausted` error, diagnose the label value (e.g. a UUID per-call label), and fix it before the plugin ships.
- The cardinality cap protects the host's Prometheus registry from unbounded metric registration (each distinct label combination is a separate series in the `GaugeVec`), which can exhaust heap and crash the host process.

The 100-value cap is chosen as a pragmatic upper bound. Real plugin metrics should have label cardinalities in the single digits (e.g. `status=ok|error`, `level=debug|info|warn|error`). A cap of 100 gives generous headroom for legitimate use cases (e.g. one label value per user role, per trigger type) while stopping runaway cases (UUIDs, request IDs, file paths) well before registry size becomes a problem.

#### 4. Metric type — all plugin-emitted metrics are Gauges

The `EmitMetric` RPC carries `(name, value, labels)` with no type discriminator. Counter and histogram semantics require declaration metadata (whether to track cumulative rate, explicit bucket boundaries) that the current RPC does not provide. `GaugeVec` is therefore the universal type: it is the safest choice when the host cannot know the plugin's intent at registration time (`metrics.go:29-34`). Plugins that need monotonic counters or histograms require a future follow-up RPC with explicit type support.

#### 5. Host-emitted plugin metrics

The host itself emits metrics in the `gleipnir_plugin_*` namespace to provide operator visibility into the plugin subsystem without requiring plugin cooperation.

**Implemented in Phase 4:**

| Metric | Type | Labels | Implemented in |
|--------|------|--------|----------------|
| `gleipnir_plugin_health_transitions_total` | Counter | `from`, `to` | `internal/plugin/state/metrics.go` |

**Specified in `plugin-system-spec.md`, not yet implemented (deferred to Phase 5):**

| Metric | Type | Labels | Spec reference |
|--------|------|--------|----------------|
| `gleipnir_plugin_rpc_duration_seconds` | Histogram | `rpc`, `plugin`, `instance` | spec §12.2 |
| `gleipnir_plugin_process_rss_bytes` | Gauge | `plugin`, `instance` | spec §12.2, §13.1 |

`gleipnir_plugin_health_transitions_total` uses `metrics.LabelFrom` and `metrics.LabelTo` label constants from `internal/infra/metrics` (`state/metrics.go:19`), consistent with ADR-037's shared label-key constants. Host-emitted metrics use `promauto.With(metrics.Registry())` to register on the same custom registry as all other Gleipnir metrics.

When `gleipnir_plugin_rpc_duration_seconds` and `gleipnir_plugin_process_rss_bytes` are implemented, they should use `metrics.BucketsFast` (defined in `internal/infra/metrics/metrics.go:59`) for the RPC histogram and `metrics.Registry()` for registration, consistent with ADR-037.

#### 6. Plugin logging — `Log` Host RPC for production; stderr capture as fallback only

Plugin log lines in production go through the **`Log` Host RPC** (`handlers.go:356-393`), not stdout or stderr. The rationale for this split is:

**Why `Log` over stdout/stderr:**
- gRPC calls from a plugin are concurrent. Multiple runs can invoke one plugin instance at the same time; a single plugin goroutine's `fmt.Println` on stdout cannot be attributed to a specific `run_id` — lines interleave arbitrarily.
- The `Log` RPC carries the `call_id` from the request context (set by `UnaryCallIDInterceptor`). When `call_id` resolves, the handler calls `logctx.WithRunCorrelation` with the associated `run_id` and `policy_id` (`handlers.go:375-376`), injecting full run correlation into every log record. The `call_id` itself is also added as a log attribute (`handlers.go:382`).
- Without `call_id`, the `Log` handler still attributes the record to `plugin` and `instance` (`handlers.go:365-368`), giving operators enough context to identify the source.
- All log paths flow through `logctx.Logger`, the host's slog pipeline (`handlers.go:391`), so plugin log lines appear in the same structured log stream as host log lines and are compatible with any log aggregator the operator uses.

**Stderr capture — fallback only:**
Stdout is reserved by the `go-plugin` handshake protocol (the magic cookie negotiation line is written to stdout; reading it would corrupt the protocol). The host pipes only stderr (`process.go:148`: `stderrW, stderrDone := PipeLines(logger, slog.LevelWarn, "stderr")`). Lines written to stderr are scanned line by line by `internal/plugin/process/logpipe.go` (`PipeLines` function) and emitted through slog at `LevelWarn` with a `stream=stderr` attribute.

Stderr capture is the fallback path for two specific situations (spec §12.1):
1. Pre-handshake panics — the plugin crashes before establishing its gRPC connection, so the `Log` RPC is not available.
2. Output written outside any active call context — e.g. init code that panics, or a goroutine that is not tracked by the dispatcher.

In normal operation, a well-behaved plugin uses `Log` exclusively and stderr is silent.

#### 7. OpenTelemetry / distributed tracing — deferred to v2

OpenTelemetry is explicitly out of scope for v1 (spec §12.2). The reasons:

- **Single-binary deployment.** Gleipnir's standard deployment is a single Docker Compose stack with no distributed services. Distributed tracing is designed for environments where a request traverses multiple services; adding OTEL to a single process adds substantial dependency weight and operational complexity (collector, exporter, sampling configuration) for no benefit.
- **No tracing pressure.** There are no current operator requirements for trace-level observability; structured logs with run/policy/call correlation (via `logctx`) are sufficient for diagnosing slow or failed runs in the homelab-scale deployment model.
- **OTEL adds dependency weight.** The `go.opentelemetry.io` module family adds ~15 dependencies and significant binary size. Gleipnir's dependency hygiene (small `go.mod`, no unnecessary transitive imports) argues against pulling this in until there is a concrete use case.

The decision is revisited when a distributed deployment model (multi-node, separate services) materializes. At that point, OTEL traces can be wired through the existing `call_id` correlation primitives as span baggage.

#### 8. Existing infrastructure reuse

The plugin observability surface deliberately reuses existing Gleipnir packages rather than building parallel systems:

| Concern | Package reused | How |
|---------|---------------|-----|
| Metric registration | `internal/infra/metrics` (ADR-037) | `metrics.Registry()`, `metrics.BucketsFast`/`BucketsSlow`, `metrics.Label*` constants |
| Log correlation | `internal/infra/logctx` | `logctx.WithRunCorrelation(ctx, runID, policyID)` in `Log` handler; `logctx.Logger(ctx)` for final emit |
| Internal event pub/sub | `internal/infra/event.Publisher` | `EmitEvent` handler publishes on the internal bus so SSE subscribers and tests observe plugin events |
| Prometheus client | already in `go.mod` via ADR-037 | no new metric backend |

### Out of scope

- Host-emitted `gleipnir_plugin_rpc_duration_seconds` and `gleipnir_plugin_process_rss_bytes`. Specified in §12.2/§13.1; not yet implemented. Phase 5 work.
- Counter and histogram types for plugin-emitted metrics. Requires a new `EmitMetricTyped` RPC or a declaration step at registration time.
- OTEL / distributed tracing. Deferred to v2.
- Per-call gRPC tracing (server interceptors that emit spans). Phase 5.
- The admin UI surface for browsing plugin log lines. Deferred.
- Log rate limiting / sampling at the Host RPC boundary. Deferred; the slog pipeline handles volume in practice for homelab-scale deployments.

### Consequences

- Plugin authors use `EmitMetric` with bare names (no prefix). Including the prefix is an error, not silently coerced.
- `plugin` and `instance` labels must not appear in user-supplied label maps — reserved by the host. Plugin authors learn this from the `reserved_label` error at development time.
- A plugin that uses high-cardinality labels (e.g. a UUID per request) will see `codes.ResourceExhausted` once it hits the 100-value cap on any label. This is intentional developer feedback, not a silent degradation.
- Plugin subprocess stderr is always wired through `logpipe.PipeLines` regardless of whether the plugin uses `Log` RPC; operators see pre-handshake panics in structured slog output without any special configuration.
- Issue #205 (dispatcher metrics) must name new host-emitted metrics in the `gleipnir_plugin_` namespace with `plugin` and `instance` labels per this ADR. The dispatcher is the natural place to implement `gleipnir_plugin_rpc_duration_seconds` (tracking per-RPC call latency from the host side).

---

## ADR-046: Audit-table split — `run_steps` vs `plugin_audit_events`

**Status:** Decided
**Date:** 2026-05

### Context

Gleipnir already has one audit substrate: `run_steps`. It records the agent's reasoning trace and is **visible to the LLM** as it executes a run (per ADR-018, the capability snapshot is the first step of every run; subsequent `tool_call`, `tool_result`, `feedback_request`, `feedback_response`, `thought`, `thinking`, `error`, and `complete` steps form the conversation context the agent sees on the next turn). Anything written to `run_steps` is part of that conversational context.

The plugin system (ADR-041) adds an entire class of operational events that have no place in the LLM's context: plugin install, signature verification outcomes, TOFU pubkey captures and admin "Accept new key" decisions, key rotations, manifest material-change blocks, credential issue/refresh/revoke, unauthorized RPC attempts, deactivate/remove, and late-callback rejections from ADR-044's `feedback_response_late` event. These are operator-and-auditor concerns. Routing them through `run_steps` would (a) leak operator-side information into agent context — for example a TOFU key-rotation rejection visible to the agent — and (b) mix two unrelated audit purposes into one table with one query path.

The plugin system spec calls this out in §12.3 as a structural decision because it determines what plugins are allowed to write where. The `WriteAuditStep` Host RPC introduced by ADR-044 lets plugins write into Gleipnir's audit substrate; without an explicit boundary, a misbehaving or malicious plugin could inject arbitrary content into the LLM's context window.

### Decision

#### 1. Two substrates, two purposes

| Substrate | Purpose | Visible to LLM? | Step / event types |
|-----------|---------|-----------------|--------------------|
| `run_steps` | LLM-relevant operations on a specific run | **Yes** — replayed into the agent's context on each turn | `capability_snapshot`, `thought`, `thinking`, `tool_call`, `tool_result`, `approval_request`, `feedback_request`, `feedback_response`, `error`, `complete` (existing v1 set) |
| `plugin_audit_events` | Operational / admin / security events about plugins and instances | **No** — never enters agent context | install, manifest changes, signature verification outcomes, TOFU pubkey events, key rotations, credential lifecycle, unauthorized RPC attempts, deactivate/remove, `feedback_response_late` (ADR-044) |

`run_steps` is unchanged from its v1 shape. This ADR does not migrate, rename, or restructure it; it only fixes the boundary.

#### 2. `plugin_audit_events` schema

The schema follows the plugin-system-spec §12.3 SQL block, adjusted to project conventions (TEXT ULIDs for cross-table references per ADR-013, TEXT ISO-8601 timestamps per ADR-003):

```sql
CREATE TABLE plugin_audit_events (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  plugin_instance_id  TEXT    NULL REFERENCES plugin_instances(id) ON DELETE SET NULL,
  event_type          TEXT    NOT NULL,
  severity            TEXT    NOT NULL CHECK(severity IN ('info','warning','high','critical')),
  actor_user_id       TEXT    NULL REFERENCES users(id) ON DELETE SET NULL,
  payload_json        TEXT    NOT NULL,
  created_at          TEXT    NOT NULL  -- ISO 8601 UTC
);
CREATE INDEX idx_pae_instance_created ON plugin_audit_events(plugin_instance_id, created_at);
CREATE INDEX idx_pae_event_created    ON plugin_audit_events(event_type, created_at);
```

The deviations from the spec block are:
- `id` is `INTEGER PRIMARY KEY AUTOINCREMENT` rather than the spec's bare `INTEGER PRIMARY KEY`. Audit-event rows are append-only and have no cross-table references, so a monotonic surrogate is the right shape — matches `openai_compat_providers` precedent.
- `plugin_instance_id` and `actor_user_id` are `TEXT` ULIDs with proper foreign keys to the actual id columns of their tables. The spec block's `INTEGER NULL` placeholders predate the project's TEXT-ULID convention.
- `created_at` is `TEXT` ISO-8601 UTC. SQLite has no real `TIMESTAMP` type; the project standardises on TEXT ISO-8601 (per ADR-003 / 0001_initial.sql).
- `ON DELETE SET NULL` on both nullable foreign keys means audit-event history outlives the deletion of a plugin instance or user — operators retain a tamper-evident trail of "an instance that no longer exists did this on 2026-05-04".

`plugin_instance_id` is nullable because some events fire before any instance row exists (e.g. signature verification on install of a previously-unknown plugin) or apply to a plugin definition rather than a single instance. `actor_user_id` is nullable because background events (fsnotify-driven installs, scheduled key rotations, automated revocations) have no human actor. `payload_json` carries the event-specific fields (e.g. `{"old_pubkey": "...", "new_pubkey": "...", "approver_user_id": 7}` for a TOFU acceptance) and is structurally validated by the writing call site, not by the database.

#### 3. `WriteAuditStep` is restricted to `feedback_response` only

The `WriteAuditStep` Host RPC introduced by ADR-044 (so plugin-provided channels can resolve a `Request` once a human replies) is the **only path by which a plugin can write into `run_steps`**, and it is restricted to a single step type: `feedback_response`. Any other step type submitted via `WriteAuditStep` is rejected with `permission_denied` and recorded as an `unauthorized_audit_step` event in `plugin_audit_events` at high severity.

This restriction is structural, not advisory. Plugins cannot write `tool_call`, `tool_result`, `thought`, `thinking`, `approval_request`, `error`, or any other LLM-visible step type — that surface is reserved for the host runtime. The host writes `tool_call` / `tool_result` itself when invoking a plugin's ToolService; a tool-result step originating from `WriteAuditStep` is by definition an attempt to inject content the host did not authorize.

All other plugin-originated events flow into `plugin_audit_events` via host-side write paths (the plugin loader, the trust manager, the credential manager). Plugins do not have direct write access to `plugin_audit_events`; the host writes on their behalf based on observed state changes.

#### 4. Authorization semantics for `WriteAuditStep`

A `WriteAuditStep(request_id, feedback_response)` call is authorized only when:
1. The calling plugin process (identified by its per-generation gRPC connection identity) belongs to the plugin instance that was originally routed the `request_id` (per ADR-044 §4 — `request_id` is instance-scoped, not generation-scoped, so a post-hot-reload generation can resolve a request issued by its predecessor).
2. The `request_id` is still open (run is in `waiting_for_feedback`, no prior response written, no timeout fired).

A mismatch on (1) is rejected as `unauthorized_request_id` (logged at high severity, `plugin_audit_events`). A mismatch on (2) — i.e. the request has already been resolved — is the late-callback path: rejected with `feedback_response_late` (ADR-044 §5), `plugin_audit_events`, normal severity. Neither case mutates `run_steps` or run state.

#### 5. Operator-facing surfaces

`plugin_audit_events` is queryable by admin and auditor roles via a new admin endpoint (deferred to the admin-UI work package; not specified here). The LLM has no read path. `run_steps` keeps its existing endpoints and SSE feeds.

### Out of scope

- Migration of any existing `run_steps` content. There is none to migrate; `plugin_audit_events` is purely additive.
- The admin UI surfaces for browsing `plugin_audit_events` (filter by event type, severity, instance). Tracked in the admin-UI work package.
- Retention / compaction policy for `plugin_audit_events`. v1 keeps everything; revisit when volumes warrant.
- Cross-substrate joins (e.g. "show me the run_step that failed because the plugin instance was unhealthy at the time"). Operators can correlate by timestamp and `run_id`; structured cross-references are deferred.
- Generic `WriteAuditEvent` Host RPC for plugins to push their own structured events into `plugin_audit_events`. v1 does not expose this; all `plugin_audit_events` writes are host-internal.

### Consequences

- A new `plugin_audit_events` table is added in the schema migration tracked by issue #184. `run_steps` is unchanged.
- The `WriteAuditStep` Host RPC handler enforces the `feedback_response`-only restriction at the boundary; an enum/oneof guard in the proto definition (issue #167) makes a wider type structurally unrepresentable on the wire.
- ADR-044's `feedback_response_late` event lands in `plugin_audit_events`. The audit split is therefore a hard prerequisite for plugin-provided channels shipping. Captured as a dependency in #184.
- Plugins have no path to inject arbitrary content into the agent's context window. The structural guarantee is the same shape as ADR-001 (hard capability enforcement): plugins cannot prompt-inject, cannot impersonate tool calls, and cannot fabricate feedback for requests they don't own.
- Operator workflows for trust events (TOFU acceptance, key rotation review) have a stable backing table from day one; the admin UI work can evolve without re-shaping the substrate.

---

## ADR-045: Plugin signing & TOFU trust

**Status:** Decided
**Date:** 2026-05

### Context

ADR-041 chose `go-plugin` subprocesses launched from a `/plugins` filesystem dropin as the v1 distribution model: no curated registry, no upload-via-UI, just operator-controlled tarballs. ADR-043 chose the signing-tooling implementation (bundled Minisign Go library at `plugin-sdk/signing`, fresh-written, no external `minisign` binary dependency, surfaced via `gleipnir-plugin keygen|sign|package` subcommands). What is still missing is the **trust model** the host applies when it sees a signed tarball: which keys are trusted, when verification runs, what counts as a manifest change worth blocking on, and what happens when verification fails.

This ADR records that model. The signing scheme itself (Minisign / Ed25519, signed payload, hash strategy) is owned by ADR-043 from the producer side; this ADR governs the consumer side — the host loader, the per-instance trust state, and the failure-mode policy. Spec sections §5.2-§5.5 are the source of truth for the matrices below; this ADR is the formal decision record.

### Decision

#### 1. Honest framing: tamper-evidence, not author identity attestation

Minisign verification proves that the bundle was signed by the holder of the captured Ed25519 private key. It does **not** prove that the holder is the named author, that the binary does what the manifest says it does, or that the author is who they claim to be. v1 buys *tamper-evidence between admin install and admin run* — a meaningful security property, but a narrower one than a curated registry with an identity layer would provide. Sigstore-keyless transparency-log verification is the v2 path once a storefront-era identity layer materializes.

The admin install gate (`plugins:install` permission, manual review of declared capabilities at `/admin/plugins`) is the bridge: the human in the loop is the identity check. This honest framing is the load-bearing assumption for every other decision in this ADR.

#### 2. Trust model: TOFU per plugin instance

Trust is captured at the instance level (one row per `plugin_instances`, see issue #185), keyed by the plugin's identity (manifest `name` + `version`-independent key). On first install of a plugin name, the embedded `signing.pub` is captured into `plugin_instances.trusted_pubkey`. All subsequent updates to that plugin must be signed by the captured key; mismatch is a hard block until an admin explicitly approves the new key via the "Accept new key" flow.

Rotation = manual admin approval. Rotation certificates (signed-by-old-key statements that authorize a successor key) are deferred to v2; in v1, every key rotation is an explicit human decision, audited as a `plugin_pubkey_rotated` event in `plugin_audit_events` (per ADR-046) with the approving `actor_user_id`.

An advanced "pin out-of-band" toggle is available at first install: an admin can paste a pubkey acquired through a separate trusted channel (e.g. a project's website over HTTPS) instead of taking the leap on the embedded one. This skips the TOFU first-install gap for security-conscious operators without changing the steady-state machinery.

#### 3. Validation timing

| Trigger                       | Action                                                                 |
|-------------------------------|------------------------------------------------------------------------|
| Install                       | Verify signature; capture pubkey (TOFU) or check against pinned pubkey; snapshot manifest into DB |
| Plugin process start          | Verify signature against snapshotted manifest                          |
| Hot-reload (`fsnotify` event) | Verify signature; if manifest has **material changes**, block reload pending admin re-approval |
| Per-RPC call                  | None — verification is process-boundary, not call-boundary             |
| Background scan               | None — next process start covers it                                    |

Per-RPC verification is deliberately omitted: the security boundary is the subprocess identity (which the host launched and connected to over a private socket), not each RPC. Adding per-call verification would burn CPU without changing the threat model.

#### 4. Material vs. cosmetic manifest changes

A hot-reload that re-verifies signature successfully is still **blocked** if the new manifest differs from the snapshotted manifest in any **material** field. The bright-line list, taken from spec §5.4:

**Material (block until admin re-approves):**
- Embedded pubkey claim
- Declared services (TriggerService / ToolService / ChannelService presence or version)
- Tier-2 Host capability declarations
- OAuth scopes, OAuth strategy
- Declared tool list (any addition, removal, or schema change)
- Per-instance `config_schema` shape
- `event_kinds[].binding_schema` shape

**Cosmetic (flow silently with audit log):**
- Description, version string, author email
- Default values inside config schemas
- JSON Schema `description` strings
- Example fixtures

Material changes raise a `pending_manifest_approval` health state on every existing instance of the plugin; the new generation does not start until an admin reviews and approves the diff. This preserves ADR-018's capability-snapshot invariant: a run that started under one declared tool surface cannot suddenly see a different surface mid-flight.

`config_schema` material changes are particularly load-bearing: the new generation cannot start until each existing instance's configuration is brought into compliance, which moves those instances into `pending_config_migration` (per spec §5.4). **No automated config migration tooling ships in v1** — admin manually edits each instance's config before the new generation activates. Migration tooling is a v2 concern.

#### 5. Failure modes

| Condition                             | Behavior                                                                         |
|---------------------------------------|----------------------------------------------------------------------------------|
| Invalid signature                     | Hard block. No override. (Distinct from "unsigned" — a signature was claimed and is wrong.) |
| TOFU violation (signed by unknown key)| Block + "Accept new key" UI; instance enters `pending_key_approval`              |
| Material manifest change on hot-reload| Block reload; instance enters `pending_manifest_approval`                        |
| Verification system error (missing `.minisig`, I/O failure) | Fail closed; surface detailed error; instance enters `verification_error` |
| Unsigned plugin                       | Block by default. See decision 6 for permissive override.                        |
| Hot-reload failure on running plugin  | Old generation drains in-flight; new generation never starts. Admin sees "serving in-flight, no new requests accepted" with View error / Revert / Remove pending update actions. |

"Block" means the new generation does not start; existing healthy generations continue serving until drained. The host never silently swaps a generation under an unverified or unapproved manifest.

#### 6. `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS` permissive-mode override

Unsigned plugins are blocked by default. Operators who need to run an unsigned local development build, or a vendored fork still being signed, can set the **global** environment variable `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true`. The semantics are deliberately blunt:

- Scope is global, not per-plugin. There is no per-plugin allowlist — that would create a slow drift toward "well, just this one more" until the trust model has rotted.
- Every load of an unsigned plugin emits a high-severity `unsigned_plugin_loaded` event into `plugin_audit_events`.
- The mode is operator-visible in the admin UI. **As shipped (v1.1.0):** rather than a global banner, each instance spawned from an unsigned bundle carries a yellow `unsigned_permissive` health chip on its row in `/admin/plugins` (see decision 7); the global `/admin/plugins` banner described in the original design was descoped in favor of the per-instance chip plus the `/api/v1/health` field below.
- `/api/v1/health` reports `signature_verification: disabled` so health-checking infrastructure can detect the mode externally.
- **Signed plugins are still fully verified** even in permissive mode. Permissive mode does not relax verification of bundles that *do* carry a signature; a tampered signed bundle is still hard-blocked. The toggle affects unsigned bundles only.

The variable is read once at host startup; runtime toggling is not supported (it would require recomputing the trust state of every loaded plugin).

#### 7. Per-instance health states

The full set of v1 health states (per spec §5.6) is enumerated here so that downstream UI work has a stable contract:

`healthy`, `signature_invalid`, `pending_key_approval`, `pending_manifest_approval`, `pending_config_migration`, `verification_error`, `unsigned_permissive`.

Each state is rendered as a colored chip on `/admin/plugins`, click-through reveals detail and admin actions (Accept new key / Approve manifest / View error / Revert / Remove pending update). Chip rendering and the action set are owned by issue #191; this ADR fixes the state names and the conditions under which the loader assigns them.

### Audit event types (issue #188)

Two audit event types are emitted by the TOFU trust machinery (both at severity `high`):

| Event type              | When emitted                                                                 | Key payload fields |
|-------------------------|------------------------------------------------------------------------------|--------------------|
| `plugin_pubkey_mismatch`| A signed update arrives with a different key than the captured trusted pubkey. The update is blocked; all instances move to `pending_key_approval`. | `plugin_id`, `name`, `old_pubkey_fingerprint`, `new_pubkey_fingerprint`, `new_pubkey_b64` (base64 of the full signing.pub bytes), `version` |
| `plugin_pubkey_rotated` | An admin accepts the new key via `POST /api/v1/admin/plugins/:id/accept-new-key`. The `trusted_pubkey` column is updated (CAS-guarded); `pending_key_approval` instances transition to `healthy`. | `plugin_id`, `name`, `old_pubkey_fingerprint`, `new_pubkey_fingerprint` |

`PluginInstanceID` is `nil` for both events (plugin-level, not instance-level). `ActorUserID` is set for `plugin_pubkey_rotated` from the authenticated session.

### Out of scope

- The Minisign Go library implementation itself (ADR-043 covers it from the producer side; the host imports the same `plugin-sdk/signing` package for verification).
- Sigstore / Rekor transparency logs. Deferred to v2 storefront era.
- Per-plugin unsigned-allow lists. Explicitly rejected (decision 6).
- Rotation certificates (signed-by-old-key authorizations of a new key). Deferred to v2; v1 = manual admin approval per rotation.
- Revocation lists. v1 has no revocation channel; admins remove a compromised plugin by uninstalling it. CRL-style infrastructure is deferred.
- Verifying plugin behavior against the manifest at runtime. Out of scope — that's a sandboxing concern (not v1).
- The admin UI flows for "Accept new key" and "Approve manifest". Owned by issues #188 (TOFU UI) and #189 (material-change detection); this ADR specifies the conditions, not the screens.
- Out-of-band pubkey paste at install time (deferred to a follow-up per issue #188 scope-down). The TOFU first-leap plus `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS` is the v1 escape hatch.

### Consequences

- `internal/plugin` (host loader) gains a verification step on install and on every process start. It imports `plugin-sdk/signing` for the verification primitive.
- `plugin_instances.trusted_pubkey` (TEXT) and a snapshotted manifest column are required by issue #185 — the trust model presumes per-instance state.
- `plugin_audit_events` (issue #184, ADR-046) is a hard prerequisite: every trust-relevant action emits an event into that table. Fail-loud rather than fail-silent is only meaningful with a recorded trail.
- `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS` joins the documented env var list in the project-level CLAUDE.md table. The per-instance `unsigned_permissive` health chip and the `/api/v1/health` field are the two operator-visible signals that the toggle is on.
- The "block on material manifest change" rule means a signed plugin update that adds a new declared tool will not auto-load — it sits in `pending_manifest_approval` until an admin reviews it. This is intentional friction; operators who want a smooth path negotiate manifest stability with their plugin authors.
- TOFU's first-install gap is acknowledged, not closed. The `plugins:install` permission gate and the optional pin-out-of-band toggle are the v1 mitigations.

---

## ADR-044: Channel routing model

**Status:** Decided
**Date:** 2026-05
**Supersedes (partially):** ADR-031 (native feedback as a Gleipnir runtime primitive) — the in-app dispatcher implementation detail is superseded; the first-class-feedback principle stands.

**User-facing vocabulary (#656):** An audience entry's "Request" routing and the agent's "feedback" capability (ADR-031) are the **same** operator-visible flow. The UI standardizes on the noun **"Feedback request"**: the audience editor keeps the concise "Request" toggle but cross-references it ("Routes an agent's feedback request to this channel.") and the routing preview reads "Feedback requests routed to:". Internal identifiers (the audience `request` field, `RouteToPlugin`/`RouteToInApp`, the `feedback_request`/`feedback_response` step types) are intentionally **unchanged** — renaming them is a deferred follow-up.

### Context

ADR-031 established that feedback is a first-class runtime primitive: the agent calls a synthetic `gleipnir.ask_operator` tool, `BoundAgent` intercepts it, the run pauses at `waiting_for_feedback`, and an operator supplies a response through the UI. The in-app UI was the only delivery surface. Notification dispatch lived in `internal/notify` as a separate fan-out concern.

The plugin system umbrella (ADR-041) extends feedback delivery to plugin-provided channels (Slack, PagerDuty, ntfy, email, etc.) and introduces a general-purpose `Notify` surface for run-state alerts alongside the existing `Request` surface for operator input. Both surfaces share a single routing abstraction — the **Audience** — that is policy-referenced and admin-managed.

This ADR records the routing model: how `Notify` and `Request` differ, how audiences are structured, the lifecycle rules for `request_id`, and the failure modes for both operation types. The proto contracts (gRPC RPC signatures, message schemas) are specified separately in issue #167; this ADR governs the semantics those contracts must implement.

### Decision

#### 1. Two channel operations with different semantics

A `ChannelService` implementation may support either or both of two operations:

**`Notify`** — fire-and-forget, parallel fan-out. The host calls `Notify` on every audience entry that has `notify: true` and whose plugin instance implements `Notify`, concurrently. A per-call deadline of 10 seconds applies. Failures from individual channels are audited and metric-counted but do NOT fail the run — the best-effort guarantee means a broken ntfy server does not stop an in-flight agent. Total latency is bounded by the single slowest channel subject to the 10s deadline, not by the count of channels.

**`Request`** — request/response, exactly one channel per request. The host routes to the first ordered audience entry that has `request: true` and whose plugin instance implements `Request`. There is no inter-channel fallback: once an entry is selected, it owns the request. The protocol is async via callback: the plugin synchronously acknowledges the request within a 5-second pre-ack deadline (confirming it received and will track the request), then later calls the host's `WriteAuditStep` Host RPC with a `feedback_response` step when the human replies.

#### 2. Audience as a shared resource

An **audience** is a named, ordered list of channel entries. Audiences are first-class admin-managed resources, editable at `/admin/audiences` (admin/operator edit; auditor read), referenced by name from policy YAML. They are shared: the same audience may be used by multiple policies.

Each entry in an audience specifies:
- `plugin_instance` — which plugin instance to route to (or the built-in `gleipnir.in-app` token).
- `notify: true/false` — whether this entry participates in `Notify` fan-out.
- `request: true/false` — whether this entry is eligible for `Request` routing.
- `config` — per-entry configuration validated against the plugin's `config_schema` (e.g. Slack channel name, mention group).

The audience editor validates the per-entry `config` against the channel plugin's manifest `config_schema` at save time. Partial implementations (a plugin that supports only `Notify`, not `Request`) cause the editor to disable the corresponding toggle with an explanatory tooltip.

When an audience referenced by one or more policies is edited, the save dialog lists the affected policies and requires confirmation. Audiences with active in-flight runs that reference them are flagged: the change applies to subsequent steps only; `Request` operations already in-flight continue to resolve against the routing that was active when they were issued.

#### 3. `gleipnir.in-app` is auto-appended to every audience

The built-in `gleipnir.in-app` channel is automatically appended as the lowest-priority entry of every audience by default. This guarantees that first-class feedback (ADR-031) always has a landing surface — an operator can always respond through the Gleipnir UI even if every plugin-provided channel is broken or misconfigured.

Audiences include an advanced toggle to disable the `gleipnir.in-app` auto-append. If disabled, the audience editor enforces a save-time validation: at least one remaining entry must have `request: true`. Disabling `gleipnir.in-app` with no remaining `Request`-capable entries is a validation error — an audience in that state could leave a `Request` operation permanently unresolvable.

#### 4. `request_id` is instance-scoped, not generation-scoped

When the host routes a `Request` to a channel, it issues a `request_id` token. The `request_id` is **instance-scoped**: it identifies the plugin instance that owns the request, not the specific subprocess generation that received it.

This distinction matters because hot-reload can occur while a `Request` is awaiting human response — the operator may not reply for minutes or hours. When the plugin is reloaded (old generation replaced by a new generation), the new generation may service the callback for a request that was issued to the old generation. The `request_id` survives the reload because it is bound to the instance, not to the generation.

Lifecycle:
- `request_id` is created when the host issues a `Request` to a channel entry.
- It remains valid as long as the feedback request is open (run is in `waiting_for_feedback`).
- If the old generation is force-killed at the 60-second drain grace without delivering a response, the open `Request` resolves via the normal feedback timeout path (existing `internal/timeout/scanner.go` machinery). Runs in `waiting_for_feedback` are NOT eagerly failed at force-kill — the timeout scanner handles expiry uniformly.
- After force-kill, the substrate (Slack, PagerDuty, etc.) may still surface the original message to a human. If the human replies, the new generation can service the callback using the same `request_id`, provided the substrate connection state is recoverable.

Authorization for `WriteAuditStep` is also instance-scoped: the host verifies that the calling plugin process (identified by its per-generation gRPC connection identity) belongs to the instance that was originally routed the request. A mismatch (e.g. a different plugin instance attempting to resolve another instance's `request_id`) is rejected and recorded as an `unauthorized_request_id` audit event at high severity.

#### 5. Late-callback rejection — `feedback_response_late` event

A `WriteAuditStep` call carrying a `feedback_response` step for a `request_id` that has already been resolved (response received through another path, or timed out) is rejected. The run state is not mutated. The host emits a `feedback_response_late` event into `plugin_audit_events` (the operational audit substrate introduced by ADR-041's audit split decision).

The `feedback_response_late` event carries: `request_id`, `plugin_instance`, `generation_id`, the `feedback_response` body that arrived late, and the timestamp. It is not surfaced in `run_steps` and is not visible to the LLM. Operators and auditors can view it in the plugin audit log.

This rule applies regardless of why the original request resolved — normal operator response, timeout expiry, or run cancellation. Once resolved, the `request_id` is closed and any subsequent `WriteAuditStep` for it is a late callback.

#### 6. Pre-ack vs post-ack failure modes for `Request`

**Pre-ack failure** — the plugin does not acknowledge the `Request` within the 5-second deadline. The host treats this as a dispatch failure: it writes a `feedback_dispatch_error` step to `run_steps` and the run fails fast. There is no retry and no fallback to the next audience entry. The operator must investigate the channel plugin and relaunch the run.

**Post-ack failure** — the plugin acknowledged the request but the human never responds (or the plugin crashes before delivering the response). This is handled by the existing `internal/timeout/scanner.go` machinery: the feedback request has a configured timeout, and if it expires without a response, the scanner resolves the request with a timeout outcome and the run continues (or fails, depending on policy configuration). The same timeout machinery handles both in-app and plugin-provided channels.

The asymmetry is deliberate: pre-ack failure means the channel never took responsibility for the request (the operator may not have been paged at all), which is a hard failure. Post-ack failure means the channel took responsibility but the human did not respond in time, which is a soft timeout the run can reason about.

### Supersedes / Preserves

**What this ADR supersedes from ADR-031:**

The dispatcher implementation detail. ADR-031's `internal/execution/agent/feedback.go` routed directly to the in-app mechanism (writing a `feedback_request` step and blocking on `feedbackCh`). This ADR supersedes that direct coupling: `feedback.go` is refactored so the in-app surface becomes one Channel implementation (`gleipnir.in-app`) among many, and the host resolves the feedback audience before dispatch. The refactor is behavior-neutral for existing deployments — `gleipnir.in-app` is auto-appended to every audience and is always the lowest-priority (and only) entry in a default single-channel deployment.

**What this ADR preserves from ADR-031:**

The first-class-feedback principle. Feedback remains a runtime primitive: `BoundAgent` still intercepts the synthetic `request_feedback` tool call, the run still pauses at `waiting_for_feedback`, and the operator's response still flows through `WriteAuditStep` → `internal/timeout/` machinery. The principle that feedback is not an MCP concept, not prompt-based, and not externally dispatched is unchanged. The in-app UI remains fully functional as the built-in `gleipnir.in-app` channel.

### Out of scope

- The proto contracts (gRPC RPC signatures, message schemas for `Notify`, `Request`, and `WriteAuditStep`). These are tracked in issue #167.
- Per-event-type routing overrides (e.g. `run_failed → pagerduty`, `feedback_request → slack-ops`). Deferred; v1 audiences are flat.
- `RecoverChannelRequests` RPC for substrate-side request recovery after generation replacement. Deferred; v1 relies on the drain grace and timeout scanner.
- Cross-plugin or cross-instance routing. Each audience entry maps to exactly one plugin instance.

### Consequences

- `internal/execution/agent/feedback.go` is refactored to route through a Channel dispatcher. The in-process `inAppChannel` becomes a Channel implementation. `runsHandler.SubmitFeedback` calls `inAppChannel.Resolve(request_id, body)`. The existing `internal/feedback/` and `internal/timeout/` machinery is unchanged; the refactor only changes what is called before the pause, not the pause mechanics.
- A new `plugin/channel` package (or equivalent) is introduced to hold the Channel dispatcher, the `gleipnir.in-app` built-in implementation, and the routing logic that consults the audience configuration.
- `request_id` tokens are issued at the host level and are durable across hot-reloads. They must be stored with enough context (instance ID, associated run, feedback request row) to allow late-callback detection and `unauthorized_request_id` checks.
- `feedback_response_late` events land in `plugin_audit_events`, not `run_steps`. The LLM does not see them. The audit split (ADR-041 decision 7) must be in place before plugin-provided channels ship.
- The `gleipnir.in-app` auto-append guarantee means no existing deployment changes behavior. The toggle to disable it is an advanced opt-out; disabling it without a replacement `Request`-capable entry is a save-time validation error.
- Audience save-guard (listing affected policies, requiring confirmation) prevents silent routing changes on shared audiences.

---

## ADR-042: Plugin service & HostAPI versioning policy

**Status:** Decided
**Date:** 2026-05

### Context

The plugin system (ADR-041, #252) introduces a gRPC service surface that third-party and community plugins must remain compatible with for years. A plugin binary may go dormant for many months between operator upgrade cycles; without an explicit versioning policy, every service change creates an ambiguous compatibility question. This ADR codifies the versioning and compatibility rules. The proto contracts themselves — concrete message definitions, RPC signatures, and field names — are deferred to issue #167. See `docs/developer/plugin-system-spec.md` §10 for the full specification.

This ADR depends on ADR-041 merging first (#252). Until that merge lands, treat this as contingent on that umbrella decision.

### Decision

1. **Per-service SemVer on four independent axes.** TriggerService, ToolService, ChannelService, and HostAPI each carry their own version. Shared protobuf messages live in `gleipnir.plugin.common.v1` and ride along with whichever service bumps them — they are not independently versioned. (spec §10.1)

2. **Three change classes.** Additive (non-breaking: new optional fields, new RPCs); Behavioral (always a major bump — a semantics shift on `Notify` becomes `NotifyV2`, no "soft semantic shift"); Removal (always a major bump, two-version deprecation window: vN deprecates with warnings, vN+1 still ships with warnings, vN+2 removes). The window is deliberately generous because operators don't watch release notes weekly and community plugins go dormant. New enum values are additive rather than breaking because plugins are required to handle a mandatory `UNKNOWN = 0` first value — this is structurally enforced by `buf lint` on every `.proto` file. Lint enforcement lives in `buf.yaml` and is gated by the `proto-lint` CI job. (spec §10.2)

3. **Plugin version vs service version are distinct.** Plugin authors set their own SemVer (`slack v2.5.0`) independently of the service versions their binary implements (`ToolService v1`, `ChannelService v1`, …). Both appear in the manifest. The plugin's own version is informational and authored by the plugin author; the service version drives compatibility and the deprecation window. Bumping plugin version alone is not a §5.4 material change unless a declared service or capability also changes. (spec §10.2 callout)

4. **HostAPI is single-version per running host in v1.** Asymmetric to plugin-side multi-version by design. Plugins must implement the host's current HostAPI; major bumps follow the same two-version deprecation window. Rationale: plugins evolve independently and may go dormant (long support windows needed); HostAPI evolves with the host (operators upgrade as a unit). Revisit if a paid third-party storefront materializes. (spec §10.4)

5. **`buf lint` as the structural enforcement gate.** `buf.yaml` at repo root selects the STANDARD lint group and FILE-level breaking-change detection. The `proto-lint` CI job (PR-only, modeled on `sqlc-drift`) runs `buf lint` on every pull request. Now active on the contracts under `plugin-sdk/proto/` introduced by #167.

### Out of scope

- The proto contracts themselves: concrete message schemas, RPC signatures, field names (#167).
- Channel routing semantics — a separate ADR.
- Plugin binary signing and TOFU key management — a separate ADR.
- Audit-table split for plugin events — a separate ADR.
- Plugin observability (metrics, structured logs from subprocesses) — a separate ADR.
- Subscribed trigger semantics — a separate ADR.
- Enabling `GLEIPNIR_PLUGINS_ENABLED` (#174).

### Consequences

- Compatibility policy is now writable into manifest validation logic and CI lint rules without re-deriving it per PR.
- The two-version deprecation window means at most three concurrent supported majors per service at any time: the version under active deprecation warning (vN), the version still shipping with warnings (vN+1), and the current version (vN+2).
- HostAPI asymmetry locks v1 out of mixed-HostAPI plugin sets — a plugin compiled against HostAPI v2 cannot run on a v1 host. Recorded as a known constraint to revisit (spec §10.4), not a bug.
- Adding a new enum value is always allowed without a major bump as long as plugins fall through to the `UNKNOWN` arm. The `buf lint` rule is the structural guarantee that makes this safe.

---

## ADR-041: Plugin system architecture (umbrella)

**Status:** Decided
**Date:** 2026-05

### Context

Gleipnir's only first-party extension surface today is MCP (HTTP transport,
capability tags host-side per ADR-004). MCP serves power users with custom
tooling but is not approachable to non-engineer extension authors and
gives Gleipnir no first-party control over OAuth flows, signed releases,
structured config, or admin UX. A second extension system is required to
grow the ecosystem without forcing every author to operate an MCP server.

The full design (process model, services, capability model, versioning,
distribution, trust model, audit substrate split, observability, DX) is
specified in `docs/developer/plugin-system-spec.md`. This ADR records the
umbrella decisions and serves as the index for the follow-up ADRs listed
in spec §16. Any detail not summarized below is governed by the spec.

### Decision

Adopt a HashiCorp `go-plugin`-based subprocess plugin system that runs
parallel to (not in place of) MCP. The umbrella records seven decisions:

1. **Process model.** Plugins are subprocesses launched by the host,
   speaking gRPC over a Unix domain socket via `go-plugin`. Filesystem
   dropin into a mounted `/plugins` directory; `fsnotify` drives
   install/hot-reload. See spec §3.1.

2. **Services.** A plugin binary may implement any subset of three
   capability surfaces — `ToolService`, `ChannelService` (Notify and/or
   Request), `TriggerService` — plus the always-present `Handshake/v1`
   and `grpc.health.v1.Health`. One binary, multiple capabilities.
   See spec §4.

3. **Capability model.** Hard capability enforcement (ADR-001) carries
   forward unchanged: plugin tools and MCP tools share one namespace
   (`<source>.<tool>`), only granted tools are registered with the
   agent, and per-policy parameter scoping (ADR-017), policy-gated
   approval (ADR-008), and capability snapshot (ADR-018) apply
   identically to plugin tools. Plugin → host RPCs are tiered (Tier 1
   always-on, Tier 2 manifest-declared and admin-approved at install,
   Tier 3 deferred). See spec §3.3 and §8.

4. **Versioning.** Per-service SemVer on four independent axes
   (`TriggerService`, `ToolService`, `ChannelService`, `HostAPI`).
   Plugin SemVer is independent of and informational relative to its
   declared service versions. Removals follow a two-major-version
   deprecation window. HostAPI ships single-version per host in v1
   (no side-by-side). The detailed deprecation policy is the subject
   of a follow-up ADR. See spec §10.

5. **Distribution.** v1 is filesystem dropin only; no curated registry,
   no upload-via-UI. New manifests land in "Pending review" and require
   admin click-through to activate. Hot-reload is supported with
   material-vs-cosmetic manifest change discipline. See spec §5.1
   and §5.4.

6. **Trust model.** Minisign (Ed25519) tamper-evidence with TOFU pubkey
   pinning at first install. Updates must verify against the pinned
   key; key rotation requires explicit admin approval. Identity
   attestation (Sigstore-keyless) is deferred to a future storefront
   era. v1 buys tamper-evidence, not author identity. The detailed
   signing/TOFU policy is the subject of a follow-up ADR. See spec §5.

7. **Audit substrate split.** Two distinct substrates with different
   audiences: `run_steps` carries LLM-relevant operations (visible to
   the LLM); a new `plugin_audit_events` table carries
   operational/admin events (install, manifest changes, signature
   outcomes, key rotations, credential lifecycle, unauthorized RPC
   attempts) and is NOT visible to the LLM. The detailed schema and
   write discipline are the subject of a follow-up ADR. See spec §12.3.

### Relationship to prior decisions

- **ADR-001 (hard capability enforcement) — carried forward unchanged.**
  Disallowed plugin capabilities are never registered with the agent.
  No prompt-based restrictions.
- **ADR-004 (MCP HTTP transport) — unchanged.** The plugin system is a
  parallel extension surface, NOT a replacement for MCP. Plugin tools
  and MCP tools share one tool namespace (`<source>.<tool>`); MCP
  remains the power-user escape hatch (spec §1, §11.6).
- **ADR-007 (BoundAgent: sensor / actuator / feedback roles) — feedback
  role fully absorbed into the Channel model.** The agent never sees
  `gleipnir.ask_operator` directly; it sees a synthetic
  `request_feedback` tool, and the runtime resolves the audience and
  routes to the appropriate Channel implementation (spec §3.3). This
  completes the supersession that ADR-031 began.
- **ADR-008 (two approval modes) — carried forward unchanged.** Plugin
  tools obey policy-gated approval identically to MCP tools; the
  runtime intercepts before invocation.
- **ADR-017 (policy-level parameter scoping) — carried forward unchanged.**
  Plugin tool parameters are narrowed per-policy before the agent
  sees them.
- **ADR-018 (capability snapshot as first run step) — carried forward
  unchanged.** The snapshot records every granted tool at run start
  regardless of whether the source is a plugin instance or an MCP
  server. Hot-reloaded plugin generations preserve this guarantee:
  in-flight runs hold a reference to the generation captured in their
  snapshot.
- **ADR-031 (native feedback) — first-class-feedback principle stands;
  in-app implementation detail superseded by Channel routing.** The
  runtime's `waiting_for_feedback` state and timeout machinery
  (`internal/timeout/`) are unchanged. What changes is the dispatcher:
  `internal/execution/agent/feedback.go` is refactored so the in-app
  surface becomes one Channel implementation among many
  (`gleipnir.in-app` is auto-appended as the lowest-priority entry per
  spec §6.2), and the runtime resolves the feedback audience before
  dispatch. The refactor is behavior-neutral (spec §15.1 step 2). The
  Channel routing model is detailed in its own follow-up ADR.

### Pending child ADRs

Spec §16 enumerates six follow-up ADRs that decide details out of scope
for this umbrella. Issue numbers for these ADRs will be assigned at
write time:

- Channel routing model (supersedes ADR-031's in-app implementation
  detail; ADR-031's first-class-feedback principle stands)
- Subscribed trigger type
- Plugin signing & TOFU trust
- HostAPI versioning policy (issue #166)
- Plugin observability surface
- Audit-table split: `run_steps` vs `plugin_audit_events`

Proto contracts (gRPC service definitions) are tracked separately as
issue #167.

### Out of scope for this ADR

This umbrella does NOT define HostAPI versioning policy in detail, proto
contracts, or any of the other follow-up ADRs listed above. It also
does not reopen any of the v1 non-goals enumerated in spec §2 and §17
(no hard sandboxing, no cross-plugin RPCs, no plugin storefront, no
user-credentials mode in v1, no OpenTelemetry, etc.).

### Consequences

- The plugin system ships parallel to MCP; both stacks coexist
  permanently. `/admin/plugins` and `/admin/mcp` are siblings (spec
  §11.6).
- A new top-level package `internal/plugin/` and a new SDK module
  `plugin-sdk/` (with its own `go.mod`) will be introduced as the
  build sequence in spec §15.1 progresses. Neither is created by
  this ADR.
- `internal/execution/agent/feedback.go` will be refactored
  behavior-neutrally to route through a Channel dispatcher, so
  plugin-provided channels can serve feedback requests without
  further runtime changes.
- A new `plugin_audit_events` table will be added in a follow-up to
  house operational events outside the LLM-visible `run_steps`
  substrate.
- Single global env var `GLEIPNIR_PLUGINS_ENABLED` (default off in
  v1, removed two minor releases later) gates the external loader;
  the in-app feedback Channel refactor lands flag-independent.
- Existing v1 invariants from ADR-001/004/007/008/017/018 remain the
  authoritative runtime guarantees; the plugin host must preserve them.
- For any detail not addressed above,
  `docs/developer/plugin-system-spec.md` is the canonical specification.

---

## ADR-040: Arcade gateway pre-authorization

**Status:** Decided
**Date:** 2026-04

### Context

ADR-039 covered transport auth (Arcade API key + `Arcade-User-ID` header injected on every MCP request). However, Arcade-style hosted brokers also require per-(user_id, tool) OAuth grants to be completed before any tool call. The existing flow surfaces auth redirect URLs to the agent at runtime, defeating autonomous operation and requiring manual intervention in the middle of a run.

### Decision

- Detection heuristic in `internal/arcade.IsArcadeGateway`: a server is treated as an Arcade gateway iff the host is `api.arcade.dev`, the path starts with `/mcp/`, and the auth headers include both `Authorization` and `Arcade-User-ID`.
- Toolkit-level pre-auth via Arcade's `/v1/auth/authorize` REST API: operators click one button per toolkit in the existing server detail panel, opening the OAuth flow in a browser tab.
- Computed `is_arcade_gateway` flag on server responses: no DB column — computed at response-build time from the URL and header names already in the response.
- New `internal/arcade/` package with no DB dependency: depends only on `internal/db` (for `db.McpTool` in toolkit grouping) and stdlib.
- Reuses ADR-039 encrypted headers for credentials: API key extracted from `Authorization` header, user ID from `Arcade-User-ID`.
- UI surface on existing MCP server detail page: `ArcadeAuthSection` rendered conditionally when `is_arcade_gateway && canManage`.
- The `/authorize/wait` endpoint uses a **10-second Arcade `wait` window** to stay safely under `GLEIPNIR_HTTP_WRITE_TIMEOUT` (default 15s). The frontend re-issues the wait endpoint in a loop until the response reaches a terminal status (`completed` or `failed`).

### Out of scope

- Per-policy or per-user `user_id` scoping (deferred per ADR-039).
- Runtime auth-required handling via the feedback channel (covered by issue #103 follow-up).
- Background auth health scanner / auto-refresh of toolkit status.
- `kind` discriminator column on `mcp_servers` — detection stays heuristic.

### Consequences

- Operators do one OAuth click per toolkit before any policy run; subsequent runs are fully autonomous.
- No schema changes: credentials reuse `auth_headers_encrypted` from ADR-039.
- `internal/arcade/` is leaf-package-shaped (depends on `internal/db` and stdlib only).
- OAuth tokens live with Arcade (server-side); Gleipnir never stores them. Grants persist across restarts and key rotations until upstream revocation.

---

## ADR-039: Per-server encrypted auth headers for authenticated MCP providers

**Status:** Decided
**Date:** 2026-04

### Context

Some MCP providers require authentication on every HTTP request — for example, a hosted MCP gateway may require an `Authorization: Bearer <token>` or `x-api-key` header carrying a per-account API token. The `mcp_servers` table previously stored only `name` and `url` — there was no mechanism to attach authentication material to a server registration. Operators working around this would have to embed credentials in the URL (query parameters), which are visible in logs and in the Gleipnir UI.

Gleipnir already has a pattern for this problem: `internal/admin` provides an AES-256-GCM encrypt/decrypt helper (keyed from `GLEIPNIR_ENCRYPTION_KEY`) used for provider API keys and webhook secrets.

### Decision

**Storage:** A new `auth_headers_encrypted TEXT` column is added to `mcp_servers`. It stores a JSON array of `{name, value}` objects, encrypted with AES-256-GCM via the existing `internal/admin` helper. The column is nullable — absence means no configured auth headers.

**API surface (write-only values):** `POST /api/v1/mcp/servers` accepts an `auth_headers` field containing an array of `{key, value}` objects (plaintext, used only at creation time). `PUT /api/v1/mcp/servers/:id` updates `name` and `url` only — it does NOT touch `auth_headers_encrypted`. Auth headers are managed per-header via write-only endpoints that mirror ADR-034's webhook-secret pattern:

- `PUT  /api/v1/mcp/servers/:id/headers/:name` — set or replace one header (admin|operator). Body: `{"value": "string"}`. The comparison against stored names is case-insensitive; the submitted casing wins.
- `DELETE /api/v1/mcp/servers/:id/headers/:name` — remove one header (admin|operator). Idempotent: no-op if the header is absent. Deleting the last header sets the column to NULL.

`GET` responses return header *names* only (`auth_header_keys`); values are never included in any response. There is no sentinel and no preserve-vs-overwrite ambiguity because edits are scoped to a single header at a time. `MaskedHeaderValue` was considered (as a bulk-PUT sentinel) and rejected before merge.

**Header name validation:** Header names are validated with `golang.org/x/net/http/httpguts.ValidHeaderFieldName` (RFC 7230 token syntax), which rejects CR, LF, NUL, colon, and all non-token characters. A fixed reserved-name list (`Mcp-Session-Id`, `Mcp-Method`, `Mcp-Name`, `Mcp-Protocol-Version`, `Content-Type`, `Accept`, `Content-Length`, `Host`) is additionally rejected — these headers are managed by the MCP client or the HTTP transport layer and must not be overridden. `Mcp-Method`/`Mcp-Name` added per the MCP 2026 realignment (`mcp-realignment-spec.md` §11 / ADR-053…ADR-060, issue #734); `Mcp-Protocol-Version` added by the `server/discover` protocol-version probe (issue #737) — it is required on every modern-transport POST and is set last in `internal/mcp.Client.post`, so reserving it here (plus an injection-time filter for rows persisted before this reservation existed) keeps an operator-configured auth header from ever colliding with it. `Mcp-Session-Id` is retained through the 12-month protocol deprecation window.

**MCP client injection:** `internal/mcp` registry decrypts `auth_headers_encrypted` when loading a server and passes the plaintext headers to the HTTP client. Every outbound request to that MCP server includes the configured headers. `internal/mcp` imports `internal/admin` for decryption — this avoids forcing every `Registry` caller (HTTP handlers, CLI commands, poll trigger) to know about the encryption key and perform decryption themselves. The existing `internal/trigger` → `internal/admin` import provides precedent for a non-leaf package importing `internal/admin`.

**`POST /api/v1/mcp/servers/test`:** The test-connection endpoint accepts `auth_headers` inline. It injects the provided headers for the one-off connection test without persisting anything. This allows operators to verify a new server configuration (including auth) before committing it.

**`gleipnirctl rotate-key`:** The key rotation command re-encrypts `auth_headers_encrypted` in the same transaction as all other encrypted columns (`provider_api_key_encrypted`, `openai_compat_key_encrypted`, `webhook_secret_encrypted`). No additional rotation path is needed.

### Out of scope

- Per-policy or per-user credential scoping. All policies that grant tools from a given MCP server share the same auth headers. Scoped credentials require URL templating and/or a new `policy_mcp_overrides` join table — deferred to a follow-up issue.
- OAuth orchestration of the upstream provider account itself. Operators connect downstream OAuth integrations in their provider's dashboard; Gleipnir holds only the static auth headers (e.g. an API key or bearer token), not the downstream OAuth tokens themselves. (See ADR-040 for the Arcade-specific in-app pre-authorization flow built on top of this.)

### Consequences

- Operators can register any MCP server that requires a static API key or bearer token without embedding credentials in the URL.
- Auth header values are never returned over the API. An operator who loses their upstream API key or bearer token must regenerate it at the source and update the Gleipnir server registration — Gleipnir provides no recovery path for the plaintext value.
- The trust expansion introduced by connecting to a hosted provider (where downstream OAuth tokens for services like Gmail and Slack reside with that provider) is operator-visible — documented in the Arcade playbook (`docs/playbooks/arcade/README.md`).
- `internal/mcp` now imports `internal/admin`. This is an intentional package boundary adjustment, consistent with the `internal/trigger` → `internal/admin` precedent.

---

## ADR-038: Atomic run-state transitions with optimistic locking

**Status:** Decided
**Date:** 2026-04

### Context

`RunStateMachine.Transition()` performed multiple DB writes sequentially without a wrapping transaction:
1. `UPDATE runs SET status = ...`
2. (Conditionally) `INSERT INTO approval_requests ...` or `INSERT INTO feedback_requests ...`

If step 2 failed, the run status was already changed in the DB with no rollback, leaving the run in an inconsistent state.

Additionally, there was no optimistic locking on the `runs` table. The state machine used an in-process mutex to guard its own `current` field, but two separate state machine instances (e.g. the agent goroutine and the timeout scanner) could both pass the `IsLegalTransition` check in memory and then both issue UPDATEs to the DB. The last write would win silently.

**Race scenario:** An approval times out (scanner transitions `waiting_for_approval → failed`) at the same millisecond the operator approves it (agent transitions `waiting_for_approval → running`). Both writes succeed; the final state is whichever UPDATE executes last in SQLite's WAL serialization order.

### Decision

**Transactions:** Wrap multi-step transitions in a DB transaction so that if `INSERT INTO approval_requests` fails, the `UPDATE runs` is rolled back.

**Optimistic locking (CAS):** Add a `version INTEGER NOT NULL DEFAULT 0` column to `runs`. Increment it on every status UPDATE:

```sql
UPDATE runs SET status = :status, version = version + 1
WHERE id = :id AND version = :expected_version
```

If `rows_affected == 0`, the transition was lost to a concurrent write — return `runstate.ErrTransitionConflict` so the caller can handle it explicitly.

`ErrTransitionConflict` lives in `internal/runstate` (not `internal/agent`) because both `agent` and `timeout` packages need it. `internal/db` cannot import `runstate` (would be a cycle), so `store.go` defines a local-package sentinel with the same string.

In-memory state (`sm.current`, `sm.version`) is updated ONLY after `tx.Commit()` succeeds, so a commit failure leaves the state machine consistent with the DB.

### Consequences

- Every transition is now durable-or-rolled-back; partial writes are impossible.
- Concurrent writers fail loudly (`ErrTransitionConflict`) instead of silently overwriting each other.
- Callers must handle the new error: timeout scanners treat it as a benign race (same as `ErrIllegalTransition`); the agent goroutine exits cleanly.
- Reviving a state machine for an existing run requires loading the current `version` from the DB and passing it via `WithInitialVersion`.

---

## ADR-036: Centralized scheduler dispatcher

**Status:** ⬜ Deferred — **NOT implemented.** The `internal/dispatcher` package described below was never built. As of v1.1.0 the live trigger subsystem deliberately retains the per-loop structure this ADR proposed to retire: `internal/trigger/scheduled.go` (Scheduler), `internal/trigger/poll.go` (Poller), and `internal/trigger/cron.go` (CronRunner) — see CLAUDE.md's `internal/trigger/` package note. The design below is preserved as a record of the proposal and the centralization may be revisited if scheduler sprawl becomes a real cost again, but no code matches it today, and `docs/developer/dispatcher.md` documents the same unbuilt design. Cron was added as a fourth per-loop trigger after this ADR was written and is not covered here.
**Date:** 2026-04

### Context

Gleipnir's trigger subsystem has accumulated two parallel implementations of "do work at time T": `internal/trigger/scheduled.go` and `internal/trigger/poll.go`. Each owns its own long-running goroutines, its own mutex/map/waitgroup lifecycle bookkeeping, and its own reconciliation plumbing. #791 recently added a `PolicyNotifier` interface with `Notify` methods on both — further duplicating the "stay in sync with DB state" plumbing across both subsystems.

The duplication has concrete costs:

- Bug #790 ("Scheduler has no reconcile loop") existed because the pattern had to be reimplemented per trigger type. When Poller got a reconcile loop, Scheduler did not. Applying the Poller fix to Scheduler (via #791) required ~100 lines of near-identical bookkeeping in each.
- Goroutine count grows with configuration: one per active poll policy plus a reconcile goroutine; one per `fire_at` timestamp on every scheduled policy.
- Adding any new timed primitive (e.g. future denial-with-reason timeouts, retry schedulers, rate-limit budgets) would repeat the pattern a third time.
- Startup wiring, shutdown draining, and `rootCtx` handoff for long-lived `Notify` calls live independently in each subsystem.

### Decision

Scheduling is centralized behind a single `Dispatcher` interface with an in-memory implementation:

```go
type Dispatcher interface {
    Schedule(fireAt time.Time, kind string, payload any) int64
    Cancel(jobID int64)
    RegisterHandler(kind string, fn HandlerFn)
}

type HandlerFn func(ctx context.Context, payload any)
```

The `memoryDispatcher` is a leaf package that owns one min-heap keyed on `fireAt`, one goroutine that sleeps until the heap top, and a handler registry populated at startup. `Scheduler` and `Poller` are retired; their logic moves into handlers registered against the dispatcher by `kind` (`scheduled_fire`, `poll_tick`). Poll is modeled as self-rescheduling — each tick handler schedules the next tick after firing.

Source of truth for pending work remains the existing tables (`policies`, policy YAML). The heap is an in-memory index rebuilt on startup by scanning those tables. Policy save paths call `Schedule()` synchronously — no reconcile loop, no notify interface, no up-to-N-seconds latency. Handlers re-check policy status at fire time and drop the fire if the policy has been paused or deleted, keeping delete/pause paths ignorant of the dispatcher.

Scope is limited to the scheduler/poll subsystems. The approval and feedback timeout scanners (`internal/timeout/`) retain their scan-for-state-change pattern, which is a better fit for "has this pending request been resolved yet?" than a per-request timer. The agent-run goroutines in `internal/run/` and `internal/agent/` are unchanged — they hold real blocking LLM/tool I/O and should remain goroutine-per-run.

### Rejected alternatives

**DB-backed scheduled_jobs table.** A new `scheduled_jobs(id, kind, payload, run_at, taken_at, completed_at)` table with a polling dispatcher. Rejected because the information already lives in existing tables (`policies.fire_at`, `approval_requests.expires_at`, policy YAML intervals) — a jobs table would duplicate rather than consolidate. DB-polling queues also scale poorly (polling pressure, row-lock contention, awkward fencing semantics under concurrent writers) and do not provide a clean foundation for future multi-node HA.

**Full event-driven refactor.** Every agent step becomes an event on a shared bus consumed by a worker pool. Rejected because the pain motivating this change is scheduler sprawl, not agent execution. Agent goroutines hold legitimate blocking I/O; converting them to event-driven workers would require reconstituting provider-specific state (tool_use_id pairing, reasoning tokens, streaming cursor position) between every step, with no corresponding win at single-node scale. The dispatcher interface is designed to compose with an event-driven layer later if it is ever warranted — it does not preclude that future.

**Replicate the #791 notify pattern for every future timed primitive.** Keep the current structure and accept that each new timed subsystem pays the same ~100-line lifecycle tax. Rejected because the cost compounds: each new primitive adds a new reconcile loop, a new notify interface, new startup wiring, and a new category of "stale-state" bug class.

### Multi-node HA path

The `Dispatcher` interface is designed for substitution. When multi-node Gleipnir is pursued, a new implementation (`leaderOnlyDispatcher`, `raftDispatcher`, or an external-primitive-backed variant using NATS JetStream, etcd leases, or Temporal) implements the same interface without caller changes. Committing to a DB-backed queue now would accrue migration debt against whatever coordination primitive is chosen later; the in-memory choice keeps that decision deferred and cheap.

### Migration

1. Add `internal/dispatcher/` package with `memoryDispatcher`, `jobHeap`, fake-clock test scaffolding, and unit tests.
2. Migrate `scheduled.go`: register `scheduled_fire` handler, seed heap from `GetScheduledActivePolicies` on startup, call `Schedule()` from the policy save path, delete the `Scheduler` struct and its `PolicyNotifier` implementation. Closes #790.
3. Migrate `poll.go`: register `poll_tick` handler that reschedules itself, seed first tick per active poll policy on startup, delete the `Poller` struct, its reconcile loop, and its `PolicyNotifier` implementation.

Design detail, diagrams, and handler contracts live in [`docs/developer/dispatcher.md`](dispatcher.md).

---

## ADR-037: Custom Prometheus registry in internal/infra/metrics (leaf package)

**Status:** Decided
**Date:** 2026-04

### Context

`github.com/prometheus/client_golang` registers collectors on a process-wide global default registry (`prometheus.DefaultRegisterer`) unless callers explicitly pass their own registry. A global registry couples every future instrumented package to init-order: if two packages register the same metric name, the second registration panics at startup and the cause can be hard to locate. It also leaks metric registrations across tests — a collector registered in one test binary persists for the lifetime of the process, causing `AlreadyRegisteredError` when a second test registers a same-named collector. The upcoming per-package instrumentation plan (spec `2026-04-09-metrics-and-logging-design`) needs one shared, explicit registry that all domain packages inject into, rather than a hidden global side channel.

### Decision

Introduce `internal/infra/metrics` as a leaf package that owns a package-private `*prometheus.Registry` (created with `prometheus.NewRegistry`, not `prometheus.NewPedanticRegistry`). The registry is initialized once in `init()` with the Go runtime collector (`collectors.NewGoCollector()`). Two exported accessors are provided:

- `Registry() *prometheus.Registry` — domain packages call `promauto.With(metrics.Registry())` to register their own collectors. The concrete type is returned (not the `Registerer` or `Gatherer` interface) so callers can use it as both without forcing separate accessors.
- `Handler() http.Handler` — returns `promhttp.HandlerFor(registry, promhttp.HandlerOpts{Registry: registry})` for mounting on the `/metrics` route in a follow-up PR.

Shared bucket presets (`BucketsFast` for MCP/DB latency, `BucketsSlow` for LLM/run duration) and label-key/enum constants (`LabelErrorType`, `ErrorTypeTimeout`, `DirectionInput`, etc.) also live in this package so the `gleipnir_` naming scheme and fixed `error_type` enum stay authoritative in one place.

### Rejected alternative

Using `prometheus.DefaultRegisterer` (the global default registry). Rejected because it couples every package to init-order, makes isolated testing awkward (registrations persist globally across tests), and obscures which registry the `/metrics` endpoint actually exposes. A custom registry makes the dependency explicit and traceable.

### Consequences

- Every future instrumented package imports `internal/infra/metrics` for the registry and shared constants. `internal/infra/metrics` imports no other internal package — it is a leaf package.
- The `/metrics` route and `GLEIPNIR_METRICS_ENABLED` / `GLEIPNIR_METRICS_PATH` env vars are added in a follow-up PR that mounts `metrics.Handler()` into the chi router.

---

## ADR-035: DB-backed system settings for runtime configuration

**Status:** Decided
**Date:** 2026-04

### Context

Gleipnir needs runtime-configurable settings that survive container restarts and image upgrades. The first such setting is `public_url` — the external URL where Gleipnir is accessible, used to construct full webhook URLs for display. Environment variables are unsuitable because they require container restart to change and cannot be edited from the UI.

### Decision

Runtime configuration is stored in the existing `system_settings` key/value table (TEXT PRIMARY KEY `key`, TEXT NOT NULL `value`, TEXT NOT NULL `updated_at`). Settings are managed via `GET/PUT /api/v1/admin/settings` (admin-only). A separate `GET /api/v1/config` endpoint exposes non-sensitive settings (currently `public_url`) to all authenticated users.

The `public_url` setting is validated on write: it must be an absolute URL with scheme and host. Trailing slashes are stripped. An empty value clears the setting via `DELETE` (not upsert — `value TEXT NOT NULL` allows empty strings, so storing `""` would be indistinguishable from "not set"). When unset, features that depend on it fall back gracefully (e.g., webhook URL display shows path-only).

### Rejected alternative

Environment variable (`GLEIPNIR_PUBLIC_URL`): rejected because it requires container restart, cannot be edited from the UI, and doesn't survive Docker image upgrades that reset env vars.

---

## ADR-034: Webhook secrets stored in encrypted DB column (scoped ADR-002 deviation)

**Status:** Decided
**Date:** 2026-04

### Context

ADR-002 established that all policy configuration lives in the `yaml` column, with only `name` and `trigger_type` as indexed columns. Webhook shared secrets are a category of data that violates this rule: the `yaml` column is returned wholesale by `GET /api/v1/policies/:id`, which means storing a plaintext secret there would expose it to any authenticated user regardless of role, and would also leak the secret into audit logs and exports.

### Decision

Webhook secrets are stored in a dedicated `webhook_secret_encrypted TEXT` column on the `policies` table, encrypted with AES-256-GCM using the key from `GLEIPNIR_ENCRYPTION_KEY`. This is an intentional, scoped deviation from ADR-002. YAML remains the source of truth for all other policy fields.

The `trigger.auth` field (`hmac | bearer | none`) **does** live in the YAML, because it is configuration (not a secret) and operators need to see and edit it in the policy editor. The encrypted secret value is never included in any policy GET response, SSE payload, or export.

### Rejected alternative

Redacting the secret from `GET /api/v1/policies/:id` response while keeping it in YAML: rejected because it creates a partial-round-trip problem — the field would silently disappear on re-save unless every edit path was made secret-aware. Storing it separately is cleaner and eliminates the surface area entirely.

### Auditor access posture

Auditors can read `trigger.auth` mode from the policy YAML (visible in `GET /api/v1/policies/:id`). They cannot call the reveal endpoint (`GET /policies/:id/webhook/secret`) or the rotate endpoint (`POST /policies/:id/webhook/rotate`), both of which require admin or operator role.

### Export / import consequences

Policy export (the YAML blob) does not include the secret. An exported policy imported to a new instance will have `trigger.auth: hmac` (or `bearer`) set but no secret — the operator must generate a new secret via the rotate endpoint. This is intentional: exporting a secret alongside a policy would undermine the encrypted-column separation.

### Legacy migration

Policies created before this change may have `webhook_secret:` in their YAML. A one-time eager startup migration (`policy.MigrateLegacyWebhookSecrets`) detects these rows, encrypts the secret into the new column, and removes the field from the stored YAML. The grandfathering rule: a policy that had a `webhook_secret` but no `auth` field defaults to `auth: hmac` (preserving original security posture). Only policies with neither field default to `auth: none`.

---

## Issue #611: Remove claudecode agent runtime

**Status:** Decided
**Date:** 2026-04
**Supersedes:** any reference to `internal/agent/claudecode/` or the `claude-code` provider

### Decision

The `internal/agent/claudecode/` subprocess runner has been deleted. The `claude-code` provider is no longer a supported policy provider. Policies that declare `model.provider: claude-code` fail at validation time with an actionable error message listing the supported LLM providers (anthropic, google, openai, openaicompat).

Existing policies stored in the database with `provider: claude-code` are not auto-migrated. They will produce a validation error on first load after deploy, prompting the operator to update the YAML. This follows ADR-001's philosophy of explicit operator action over silent behaviour changes.

`(c *Client) ServerURL()` in `internal/mcp/client.go` was also removed as it was only consumed by the deleted ClaudeCodeAgent.

---

## ADR-016: Real-time UI transport: SSE over WebSockets

**Status:** Decided
**Date:** 2026-03 (addendum 2026-04)

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

**Consequence:** The Go SSE handler must flush each event immediately. Since the frontend is
now served directly by the Go HTTP server (ADR-006 revised), there is no nginx buffering layer
to contend with — the `http.Flusher` interface in the SSE handler is sufficient.

**Addendum (2026-04): Reconnection semantics**

Native `EventSource` cannot set request headers, so it cannot send `Last-Event-ID` on
reconnect. The frontend reconnection path is therefore implemented with `fetch` +
`ReadableStream` in the `useSSE` hook. Every event carries a monotonic `id:` field; on
reconnect the client sends `Last-Event-ID: <id>` and the server replays any buffered events
with a higher id.

Backoff schedule: 1s → 2s → 5s → 15s, held at 15s on further failures, reset to 1s on any
successful connect. The UI shows `reconnecting` for failures 1–4 and transitions to
`disconnected` only on the 5th consecutive failure (i.e. after the first 15s retry itself
fails). An idle watchdog aborts and reconnects the stream if no bytes (including the 15s
`: keepalive` heartbeats) arrive within 30 seconds — this recovers from silent TCP half-close
on mobile / VPN paths. This addendum does not supersede ADR-016; it documents the
client-side contract the Go handler already implements.

**Addendum (2026-04): Drop observability and buffer sizing**

When a subscriber's per-client channel buffer is full, the broadcaster drops the event for
that subscriber rather than blocking delivery to all other subscribers. This is a hard
guarantee, not a best-effort hint. The new counter `gleipnir_sse_events_dropped_total`
(no labels, matching the unlabelled style of `gleipnir_approval_timeouts_total`) is the
production signal for undersized buffers. A sustained non-zero rate means clients are
receiving events faster than they can drain their channel and the defaults should be raised.

The documented recovery path for a client that missed events is automatic. On reconnect the
frontend sends `Last-Event-ID` and the server's `Replay` method returns every buffered event
with a higher id. Per-route REST queries (`useRuns`, `useRunDetail`, `useRunSteps`, etc.)
invalidate on each SSE event via TanStack Query and act as a reconciliation layer when the
replay window is exceeded — the UI re-fetches current state and converges even if some events
were dropped and the ring buffer has already overwritten them.

Default buffer sizes were raised to 256 per-subscriber and 2048 ring (from 64 and 512
respectively). The original defaults were chosen before per-step event streaming; under load
they dropped too readily. These are code defaults, not env vars, to keep the operational
surface small. `WithChannelSize` and `WithRingSize` remain the escape hatch for tests and
future tuning without adding `GLEIPNIR_SSE_*` environment variables that have no concrete
per-deployment need today.

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

**Amendment (2026-08, #745/#769):** The "Validator warns at save time if a param name
doesn't appear in the tool's discovered input schema" bullet above is superseded, subject
to the two qualifiers below. Under ADR-059's canonical-schema regime, `params` keys are
checked against the tool's stored canonical schema (`mcp_tools.canonical_schema`) rather
than the raw discovered schema — but ONLY for a tool reference that resolves against
`mcp_tools` (i.e. a row exists for `server_name.tool_name`).

**Superseding amendment (2026-08, #769 — option 3 chosen):** #788 originally made these
checks BLOCKING rejections at policy save (#769's "option 2 — fail loudly"). That was
reverted deliberately: it blocked legitimate saves, most importantly for any MCP server
not rediscovered since the `canonical_schema` column landed — a fleet-wide condition an
operator cannot fix from the policy editor. **A `params` block now never blocks a save.**
Each case emits a non-blocking warning on `SaveResult.Warnings` instead:

- an unresolvable `params` key warns that it narrows nothing (and that, if it is the only
  key, the tool ends up accepting no arguments at all);
- a canonical schema branching at the top level (`oneOf`/`anyOf`/`allOf`/`$ref`/`not`/`if`)
  warns. Narrowing reaches only top-level properties: where the schema also declares them
  they are still narrowed and enforced and only branch-nested properties escape; where it
  declares none, scoping does nothing at all;
- no stored canonical schema warns that scoping was not verified at save, while noting
  narrowing still runs at runtime against the raw schema
  (`mcp.ResolvedTool.SchemaForNarrowing` falls back);
- granting the tool without `params` stays silent in every case.

**This is a conscious security trade, and it must not be described as structural
enforcement without qualification.** For a tool whose schema has no top-level
`properties` — including a root-level `oneOf`/`anyOf` — `mcp.NarrowSchema` returns the
schema unchanged and `mcp.ValidateCall` permits every key, so an agent may pass any
argument the tool accepts. The warning is the operator's only signal, and the frontend
does not currently render warnings, so today it reaches API/CLI clients only. Real
narrowing into branch keywords (#769 option 1) is deferred pending a fuller
investigation; #769 stays open to track it.

*Qualifier 1 — save-path carve-out.* A tool reference that does NOT resolve against
`mcp_tools` at all is NOT covered by the rejection above; its `params` block is saved
unvalidated, with only a non-blocking warning. This covers four cases indistinguishable
from each other at the policy-service layer: a genuine plugin-sourced tool (plugin tools
are never registered in `mcp_tools`, and shipped plugin policies rely on this), a
misspelled tool reference, a tool on an MCP server that has never been
discovered/refreshed, and a tool on a server not yet registered. The latter two create a
save-ordering gap: a policy saved today with an unvalidated `params` block against a
not-yet-registered server can become exploitable later, without the policy being
re-saved, the moment that server is registered and discovered with a branching or
otherwise-unnarrowable schema. A reliable plugin-vs-MCP classifier at the policy-save call
site would close this gap; the one candidate evaluated for #745 — `toolregistry.Registry`,
the in-memory cross-source namespace arbiter — was rejected for the purpose: it is not
reliably populated at policy-save time (empty on every process restart until each MCP
server is manually refreshed; released the moment a plugin instance is deactivated or its
subprocess stops). This is the identical reason `main.go`'s `manifestClassifier`
deliberately classifies plugin tools from the installed-instance row and manifest snapshot
rather than the arbiter (#399) — using the arbiter here would reintroduce the same
liveness-coupled misclassification #399 already fixed elsewhere. Closing this gap needs a
static (DB + manifest) classifier threaded into `policy.Service`; tracked as a follow-up,
not fixed in #745.

*Qualifier 2 — save-time only.* Even for a tool that DOES resolve against `mcp_tools`,
this rejection is enforced only at the moment the policy is saved. `RefreshTools`
(`internal/mcp/registry.go`) unconditionally overwrites `input_schema` and
`canonical_schema` for a server on every refresh — including writing NULL when the newly
discovered schema fails canonicalization — with no re-consent and no re-validation of
policies that already grant that tool with a `params` block. An MCP server update
(compromised or benign) that introduces a top-level branch keyword after a policy was
saved and accepted therefore disarms narrowing for every future run of that policy,
silently, until the policy is re-saved.

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

## ADR-006: React frontend, embedded in Go binary

**Status:** Decided (revised)
**Date:** 2026-03

**Decision:** React + TypeScript app (Vite build) is embedded directly into the Go binary via
`go:embed` and served by the Go HTTP server. The Docker build uses a multi-stage Dockerfile:
Node builds `frontend/dist/`, then the Go stage copies it in before `go build` so the embed
directive captures it. nginx and a separate frontend container are eliminated. YAML editor uses
CodeMirror 6 (`@codemirror/lang-yaml`). Response envelope: `{ data: T }` for success,
`{ error: string, detail?: string }` for failure.

**SPA routing:** The Go server registers a catch-all `/*` route (`frontend.NewSPAHandler`) that
serves static assets directly and falls back to `index.html` for unknown paths, enabling
client-side routing.

**Caching:** Assets under `assets/` (Vite's hashed filenames) are served with
`Cache-Control: public, max-age=31536000, immutable`. `index.html` is served with `no-cache`.

**Design system:** IBM Plex Sans (body) + IBM Plex Mono (code/values). Dark theme with layered
backgrounds (`#0F1117` → `#131720` → `#1E2330`). Semantic colors: blue (sensors/running),
orange (actuators), amber (approvals), green (success), red (errors), purple (feedback/interrupted),
teal (poll). Full design token reference in `docs/Frontend_Roadmap.md`.

**Design reference:** Design tokens and visual language are defined in `frontend/src/tokens.css` and documented in `frontend/CLAUDE.md`.

**Reasoning:** Eliminates the nginx container, reducing the deployment footprint to a single
container. The Go binary becomes the sole deliverable — simpler ops for homelab deployments.
CodeMirror 6 chosen over Monaco for bundle size (~30KB vs ~2MB).

**Related:** ADR-016 (SSE), ADR-019 (dual-mode editor), ADR-020 (folders), ADR-021 (discovery diffs).

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

## ADR-019: Dual-mode policy editor (form + YAML)

**Status:** Decided
**Date:** 2026-03

**Decision:** The policy editor provides two modes toggled by a Form/YAML switch. Both modes
edit the same underlying YAML string. The form view parses YAML into structured fields (name,
description, folder, trigger, capabilities with tool picker, task instructions, limits,
concurrency). The YAML view is a CodeMirror 6 editor with syntax highlighting and validation.
Switching modes syncs data bidirectionally.

**Reasoning:** Raw YAML editing is powerful for operators who know the schema, but a form view
with a tool picker dramatically lowers the barrier for creating and editing policies. The
dual-mode approach serves both audiences without maintaining two data models — YAML remains
the single source of truth (ADR-002), and the form is a structured view into it.

**Consequence:** The frontend must include YAML parse/serialize logic. The form view requires
`GET /api/v1/mcp/servers` and tool list endpoints to populate the tool picker.

---

## ADR-020: Policy folders for UI grouping

**Status:** Decided
**Date:** 2026-03

**Decision:** Policies have an optional `folder` field in their YAML (default: "Ungrouped").
The dashboard groups policies into collapsible folder rows. Folders are purely cosmetic
organizational labels — they have no effect on trigger routing, runtime behaviour, or
access control.

**Reasoning:** As the number of policies grows, a flat list becomes hard to scan. Folders
provide lightweight organization without introducing a separate entity in the data model.
Storing folder as a YAML field (not a DB column) keeps the schema simple and consistent
with ADR-002 (policy-as-YAML). The dashboard derives folder groupings at read time.

**Rejected alternative:** Folders as a separate DB table with a foreign key on policies.
Rejected because folder membership has no runtime semantics — it's a UI-only concern and
doesn't justify a data model change.

---

## ADR-021: MCP discovery diffs

**Status:** Decided
**Date:** 2026-03

**Decision:** When `POST /api/v1/mcp/servers/:id/discover` is called, the response includes
a diff showing tools added, removed, and modified since the last discovery. The frontend
renders this as a visual diff with accept/assign actions. This is manual, operator-initiated
re-discovery — not automatic drift detection.

**Reasoning:** MCP servers evolve over time. When an operator updates an MCP server container
and re-discovers, they need to see what changed and assign roles to new tools. Showing a diff
is far more useful than silently updating the tool list. It also surfaces affected policies
(those referencing removed or modified tools) so the operator can update them.

**Consequence:** The discovery endpoint must compare the new tool list against the existing
registry and return a structured diff. Added tools need role assignment before they can be
used in policies.

---

## ADR-022: ProviderWire seam + cross-wire contract suite

**Status:** Decided (undeferred 2026-06, issue #506)
**Date:** 2026-03 (original deferral); revised 2026-06

**Original decision (deferred):** Test infrastructure should inject a fake `http.RoundTripper`
rather than adding interface seams to production types. Deferred because `agent.Config.MessagesOverride`
was the blocking prerequisite.

**Revised decision:** A `ProviderWire` interface is introduced in `internal/llm` as a shallow
provider-level seam. Each provider adapter (anthropic, openai, google, openaicompat) implements
`ProviderWire` with methods `Call`, `Stream`, `ListModels`, `ValidateModelName`, `ValidateOptions`,
`InvalidateModelCache`, `ClassifyError`, and `ProviderName`. A shared `ProviderAdapter` wraps any
`ProviderWire` and owns the metrics-defer choreography in `CreateMessage` (timer, `ObserveRequestDuration`,
`RecordError`, `RecordTokens`). `StreamMessage` delegates directly.

**Test infrastructure:** `FakeWire` is a fifth `ProviderWire` implementation that returns scripted
responses and captures requests. `testutil.NewFakeClient` wraps a `FakeWire` in a `ProviderAdapter`
so tests get a real client that exercises the metrics path. `testutil.NewFakeClientOnly` discards
the wire handle for inline use. `MockLLMClient` is retained for the `hookOnceLLMClient` pattern
in `agent_test.go` which requires the concrete type; all other test call sites migrated to
`NewFakeClient` / `NewFakeClientOnly`.

**Cross-wire contract suite:** `internal/llm/contract` contains a table-driven test suite
(`package contract_test`) parameterized over all four real wires. It asserts the
provider-agnostic invariants uniformly: stop-reason normalization (end_turn / tool_use /
max_tokens), usage extraction (InputTokens / OutputTokens / ThinkingTokens), tool-name
round-trip (sanitization + reverse-mapping), and continuity-state carriers (per-provider,
explicitly non-uniform per BLOCKING-3):
- anthropic / openai: `ThinkingBlock.ProviderState` JSON round-trip
- google: `ToolCallBlock.ProviderMetadata["google.thought_signature"]` (no ProviderState)
- openaicompat: ThinkingBlocks are DROPPED (Chat Completions has no reasoning round-trip;
  the contract test asserts they do not appear in the outbound request)

**Boundary preserved:** `LLMClient` interface and agent runtime are unchanged (ADR-026). The
`ProviderWire` seam is entirely inside `internal/llm`; `internal/execution/agent` imports
`internal/llm` only, never provider packages.

**Zero-value safety (BLOCKING-1):** The four model/option methods (`ValidateOptions`,
`ValidateModelName`, `ListModels`, `InvalidateModelCache`) are kept as thin explicit forwarders
on each concrete Client type, never promoted from the embedded `*ProviderAdapter`. A zero-value
`&AnthropicClient{}` / `&Client{}` / `&GeminiClient{}` is still safe to call model methods on.

**Consequences:**
- `internal/llm/wire.go` — `ProviderWire` interface definition
- `internal/llm/adapter.go` — `ProviderAdapter` + metrics-defer
- `internal/llm/fake_wire.go` — `FakeWire` + `NewFakeWire`
- All four provider `client.go` files restructured with an inner `wire` / `compatWire` type
- `internal/testutil/mock_llm.go` — `NewFakeClient`, `NewFakeClientOnly` added
- `internal/llm/contract/` — new package with cross-wire contract suite
- All test call sites in execution, trigger, run, admin, http packages migrated from
  `NewMockLLMClient` to `NewFakeClient` / `NewFakeClientOnly`

---

## ADR-023: Per-policy model selection

**Status:** Decided
**Date:** 2026-03

**Decision:** Policies may declare an optional `agent.model` field selecting which Claude model
the agent uses. If omitted, the default is `claude-sonnet-4-6`. The field is validated at save
time against a local allowlist of three known model IDs, with an additional blocking API-level
check via `client.Models.Get`. The selected model is recorded in the capability snapshot
(alongside the tool list) so every run's audit trail captures the exact model used.

**Supported models:** `claude-opus-4-6`, `claude-sonnet-4-6`, `claude-haiku-4-5-20251001`.

**Rejected alternative:** Storing a system-wide default model in server config. Per-policy
selection gives operators the ability to match model capability to task complexity without
centralizing the decision.

**Consequences:**
- `internal/model.AgentConfig` gains a `Model string` field.
- `internal/policy` gains a `ModelValidator` interface and `AnthropicModelValidator` implementation.
- `internal/policy.NewService` signature updated to accept `ModelValidator` as a third argument.
- `internal/agent`: `MessageNewParams.Model` uses `anthropic.Model(a.policy.Agent.Model)` instead of
  the hardcoded `anthropic.ModelClaudeSonnet4_6` constant.
- Capability snapshot content shape changes from `[]GrantedTool` to `{model string, tools []GrantedTool}`.
  Frontend handles both shapes for backward compatibility with snapshots written before this change.

---

## ADR-024: Webhook HMAC-SHA256 signature verification

**Status:** Decided
**Date:** 2026-03

**Decision:** Webhook policies may declare an optional `trigger.webhook_secret` field (minimum 32
bytes). When set, every incoming `POST /api/v1/webhooks/{policyID}` request must include an
`X-Gleipnir-Signature: sha256=<hex>` header. The signature is the HMAC-SHA256 of the raw
request body using the configured secret. Comparison is timing-safe (`hmac.Equal`).

**Backward compatibility:** Policies without `webhook_secret` continue to accept requests with no
signature header (open webhook behaviour). Setting a secret does not break existing callers that
haven't yet been updated — until the operator sets the secret, the endpoint remains open.

**Response codes:**
- Secret configured, no header → 401 Unauthorized
- Secret configured, wrong signature → 403 Forbidden
- Secret configured, valid signature → proceed normally
- No secret configured → proceed normally (no header required)

**Rate limiting:** The webhook route is additionally protected by a per-process concurrency
throttle of 10 in-flight requests (`chi/middleware.Throttle`). This is applied only to the
webhook route, not globally.

**Secret length:** Minimum 32 bytes enforced by the policy validator. Shorter secrets are
rejected at save time with a clear error message.

**Secret storage:** `webhook_secret` is stored in the policy YAML blob (ADR-002). The
`TriggerConfig.WebhookSecret` field is tagged `json:"-"` to prevent the secret from appearing
in SSE events, run steps, or any JSON serialization of the config.

**Rejected alternative:** A shared global webhook signing key. Per-policy keys allow operators
to rotate secrets for individual integrations without affecting others.

---

## ADR-015: Policy Concurrency Model

**Status:** Decided
**Date:** 2026-03

**Decision:** v1.0 supports two concurrency modes, configured per policy in the `concurrency`
block of the policy YAML:

- **Skip** — if a run for this policy is already active (status `pending` or `running` or
  `waiting_for_approval`), the incoming trigger is dropped. The webhook still returns 202
  Accepted, but no run is created. The response body indicates the trigger was skipped and
  includes the ID of the currently active run.
- **Queue** — if a run is already active, the incoming trigger payload is held in a
  per-policy queue. When the active run reaches a terminal state (`complete`, `failed`,
  `interrupted`), the next queued payload is dequeued and a new run is created from it.
  Queue depth is bounded (default: 10 entries); payloads arriving when the queue is full
  are dropped with a 429 response.

`skip` is the default if no `concurrency` block is specified.

**Deferred to v1.1:** `parallel` (allow N concurrent runs up to a configured limit) and
`replace` (cancel the active run and immediately start a new one from the incoming trigger).
Both are architecturally compatible with the skip/queue implementation — they share the same
active-run detection path and require only additional branch handling.

**Policy YAML shape:**
```yaml
concurrency:
  mode: skip | queue
  queue_depth: 10    # only meaningful when mode is queue
```

---

## ADR-026: Model-Agnostic Design (Multi-Provider) — Revised

**Status:** Decided (revised 2026-03)
**Date:** 2026-03
**Supersedes:** Original ADR-026 (Model-Agnostic Design)

**Decision:** The LLM client is abstracted behind an `LLMClient` interface with three methods:
`CreateMessage` (stateless request/response translator), `StreamMessage` (returns a channel of
response chunks), and `ValidateOptions` (validates provider-specific policy options at save time).

The interface is a **stateless translator** — one request in, one API call, one response out. No
memory of previous calls. No decisions. The `BoundAgent` is the orchestrator: it owns the
conversation loop, conversation history, tool call routing, approval interception, audit trail,
and loop termination.

**v1.0 ships two providers:** Anthropic (Claude) via `anthropic-sdk-go` and Google (Gemini) via
`google.golang.org/genai`.

**Core types:** `MessageRequest` carries system prompt, full conversation history (provider-neutral
`ConversationTurn` slices), tool definitions (MCP-native JSON Schema), and optional `ProviderHints`.
`MessageResponse` returns ordered `ContentBlock` slices (text + tool calls interleaved), `StopReason`,
and optional `TokenUsage`. `MessageChunk` supports streaming with a `Done` flag and error channel.

**Provider hints:** Typed, provider-specific config via `ProviderHints` struct with `*AnthropicHints`
and `*GoogleHints` fields. Anthropic hints include `EnablePromptCaching` and `MaxTokens`. Google
hints include `EnableGrounding` and `ThinkingLevel`. All fields are pointers; nil means use default.

**Policy YAML model section:**
```yaml
model:
  provider: anthropic
  name: claude-sonnet-4-20250514
  options:
    enable_prompt_caching: true
```

The `provider` field selects the `LLMClient` implementation. The `name` field is the model identifier.
The `options` map is translated into `ProviderHints` at parse time — unknown options are validation
errors. A policy omitting `model` entirely uses the system default (configurable via env var,
defaulting to `anthropic/claude-sonnet-4-20250514`).

**Boundary of responsibilities:**

*BoundAgent owns:* conversation state (full history in neutral format, passed on every call), loop
termination (max turns, max tokens, timeout), tool call routing (MCP registry dispatch, parameter
validation per ADR-017, approval interception per ADR-028/029), parallel tool call batching, audit
trail, error handling, and conversation structure discipline (strictly alternating turns).

*LLMClient implementations own:* SDK interaction and auth, schema translation (MCP JSON Schema →
provider-native tool format), conversation format translation (roles and content blocks), response
normalization, tool call ID guarantees (synthetic UUIDs when provider returns empty IDs), error
result translation (`IsError` → provider convention), error mapping (rate limits, auth failures),
provider hints application, and option validation.

**Validation wiring:** A provider registry (keyed by name string) is created at startup holding all
`LLMClient` implementations. The policy validator receives this registry via DI and calls
`ValidateOptions` at save time — the policy package never imports provider SDKs.

**Package structure:** `internal/llm` contains the interface and shared types. `internal/llm/anthropic`
and `internal/llm/google` contain the two implementations. `internal/agent` imports `internal/llm`
for the interface; it never imports provider SDKs directly.

**Rejected alternatives:**
- Per-provider BoundAgent implementations — duplicates 5-10x more orchestration logic than it saves
- Neutral ToolDef intermediate struct — premature with two providers
- Stateful interface with internal conversation management — loses audit visibility
- Single-method interface (no streaming) — adding methods later is breaking
- CountTokens method — deferred to v1.1
- Limits in the interface — loop control is a BoundAgent responsibility

**Consequences:**
- `internal/llm` package created with interface, shared types, and two implementations
- `internal/agent` imports `internal/llm`, never provider SDKs
- Provider registry created at startup, injected into policy validator and trigger engine
- Policy parser validates `model` section including provider options via `ValidateOptions` at save time
- Audit trail records `provider` and `model_name` on every run record
- Capability snapshot (ADR-018) records tools in MCP-native `ToolDefinition` format
- Adding a new provider requires: implementing `LLMClient`, adding a `ProviderHints` field, registering in the registry — no BoundAgent changes

**Amendment (2026-04):** `ThinkingBlock` now uses opaque `ProviderState json.RawMessage` instead
of named provider-specific fields (`Signature`, `RedactedData`, `EncryptedContent`, `ID`). Each
provider package that has round-trip state defines its own unexported state struct and
marshal/unmarshal helpers; the shared interface carries only `Provider`, `Text`, `Redacted`, and
`ProviderState`.

Rationale: named fields created a lowest-common-denominator leak that grew with each new provider.
Opaque bytes scale to additional providers without touching the shared interface.

Per-provider adoption (not mandated uniformly):
- `internal/llm/anthropic`: defines `anthropicThinkingState{Signature, RedactedData}`. Round-trips
  via signature (non-redacted) or redacted-data (redacted blocks).
- `internal/llm/openai`: defines `openaiThinkingState{ID, EncryptedContent}`. Round-trips via the
  Responses API reasoning item ID and encrypted content.
- `internal/llm/google`: has no `ThinkingBlock` round-trip state today (its thought signature lives
  on `ToolCallBlock.ProviderMetadata["google.thought_signature"]`, out of scope). No state struct;
  its `ThinkingBlock` constructions compile unchanged.
- `internal/llm/openaicompat`: drops thinking blocks entirely. No state struct.

What does NOT change:
- `ProviderHints any` — typed-per-provider; request-time config where caller ergonomics favor a
  typed interface over opaque bytes.
- `ToolCallBlock.ProviderMetadata map[string][]byte` — already opaque bytes; map shape lets
  independent keys coexist (Google uses one key).

Cross-provider semantics: a block whose `Provider` does not match the current provider (empty or
mismatched) is silently skipped (Debug log). Empty `ProviderState` (nil or len 0) is also skipped
— treated as text-only with no round-trip data. Malformed `ProviderState` JSON returns an error
and the agent fails the run with a wrapped message — do not silently drop continuity.

Destructive migration: no DB schema change (`ThinkingBlock` provider-specific fields were never
persisted — the audit writer records only `{text, redacted}`). Fresh installs only for in-flight
conversations.

**Amendment (2026-06, issue #506):** The `LLMClient` interface and `internal/execution/agent`
runtime are confirmed unchanged by the ProviderWire refactor (ADR-022). The seam is internal to
`internal/llm`; the agent boundary remains `LLMClient`. Adding a new provider requires implementing
`ProviderWire` (not `LLMClient`) and registering in `internal/llm/factory`. The `ProviderAdapter`
handles metrics uniformly so new providers inherit observability without per-provider boilerplate.

---

## ADR-028: Tool Risk Classification Model

**Status:** Decided
**Date:** 2026-03

**Decision:** Tool risk is expressed exclusively via per-tool `approval` configuration in the
policy YAML. There is no risk level abstraction (safe / elevated / critical), no tag system,
and no category-level default behavior. Every tool's approval requirement is stated explicitly
by the policy author at the point of use.

**Policy YAML shape:**
```yaml
tools:
  - tool: kubectl.get_pods
    params:
      namespace: ["worker-01", "worker-02"]

  - tool: kubectl.delete_pod
    approval: required
    params:
      namespace: ["worker-01", "worker-02"]

  - tool: mealie.search_recipes
    # no approval field — defaults to not required
```

The `approval` field on a tool entry accepts:
- `required` — the tool call is intercepted before execution; an operator must approve
- absent / omitted — no approval gate; the tool executes immediately

**Deferred:** Risk level labels (safe / elevated / critical) as optional metadata for UI
grouping and default-approval inference. If introduced in a later version, they will be
additive — the per-tool `approval` field remains the runtime primitive and any risk label
would only influence the form editor's defaults, never override an explicit per-tool setting.

**Reasoning:** The sensor/actuator distinction (original ADR-007) provided implicit risk
classification — sensors were implicitly safe, actuators were implicitly risky. With that
distinction removed, the temptation is to replace it with an explicit risk taxonomy. This
adds complexity at both the schema and runtime layers without providing meaningful benefit
for v1.0: the policy author already knows which tools are dangerous in their environment,
and making that judgment explicit in the policy is clearer than inferring it from a category.
A `kubectl.get_pods` call is safe in most contexts; in a policy with write-access to a
production cluster it may warrant approval. Only the policy author can make that call.

**Rejected alternatives:**
- Risk levels with runtime effect: adds a layer of indirection between what the YAML says
  and what the runtime does. Hard to reason about, harder to audit.
- Tags with policy rules keyed off them: significant schema complexity for v1.0 with no
  clear benefit over per-tool config.

**Consequence:** The policy schema `tools` entries have two fields beyond the tool reference:
`approval` (optional, `required` or absent) and `params` (optional, see ADR-017). No
additional fields or tables are needed. The runtime approval interceptor in `BoundAgent`
checks the per-tool approval flag directly from the parsed policy — no lookup into a
risk registry.

---

## ADR-029: Approval State Machine (v1.0 Minimal)

**Status:** Decided
**Date:** 2026-03

**Decision:** The v1.0 approval gate is a two-outcome gate: approve or deny. No reason field,
no agent feedback path, no per-tool timeout configuration.

**Approve path:**
1. `BoundAgent` intercepts the tool call, sets run status to `waiting_for_approval`, writes
   an `approval_request` step to the audit trail.
2. The SSE stream emits `approval.created` — the UI surfaces the request to any user holding
   the Approver role.
3. The operator clicks Approve in the UI, which calls `POST /api/v1/runs/:run_id/approval`
   with `{"decision": "approved"}`.
4. The approval decision is written as an `approval_decision` step in the audit trail.
5. `BoundAgent` unblocks, calls the MCP server, returns the result to Claude, sets run status
   back to `running`.

**Deny path:**
1. Same interception and notification as the approve path.
2. The operator clicks Deny.
3. The decision is written as an `approval_decision` step with `outcome: denied`.
4. `BoundAgent` unblocks, sets run status to `failed`, writes an `error` step with a
   structured failure record indicating which tool was denied and at which step.
5. The run terminates. Claude is not informed — the run simply ends.

**Timeout behavior:** A fixed global timeout applies to all approval gates (default: 30
minutes, configurable via environment variable at the instance level). On timeout, the
outcome is `denied` — the same path as an explicit denial. No auto-approve option in v1.0.

**Deferred to v1.1:**
- Denial with reason: operator provides a reason string; the reason is fed back to Claude
  as a structured tool result and the run continues rather than terminates.
- Denial hard-stop vs denial-with-reason as distinct outcomes (the full ADR-029 state
  machine).
- Per-tool timeout duration and per-tool timeout outcome (auto-approve vs auto-deny).
- Timeout with reason (auto-deny and inject a canned reason into the agent context).

These are additive changes. The approve/deny channel between the HTTP handler and
`BoundAgent` is designed as a typed struct (`ApprovalDecision{Outcome, Reason}`) from day
one — even though `Reason` is unused in v1.0, the channel shape does not need to change
when denial-with-reason is added.

**Reasoning:** The full approval state machine (PAT-005) is one of Gleipnir's strongest
product differentiators. It is deliberately deferred — not because it is unimportant but
because shipping a minimal gate first keeps the v1.0 surface area manageable and ensures
the audit trail, SSE notification, and UI approval flow are solid before adding the
complexity of agent-adaptive denial handling.

**Consequence:** `ApprovalDecision` struct carries `Outcome` (approved / denied / timeout)
and `Reason` (string, unused in v1.0 but present for forward compatibility). The
`approval_decision` step content records `outcome` and `tool_name`. A `run_approvals` table
(or equivalent column on `run_steps`) records the wall-clock time between `approval_request`
and `approval_decision` for future approval analytics. The global timeout is implemented as
a `time.After` in the `BoundAgent` approval wait loop.

---

## ADR-030: UI abstracts over tool transport — "Tools" page is protocol-agnostic

**Status:** Decided
**Date:** 2026-03

**Decision:** The frontend uses "Tools" as the page name, navigation label, and route (`/tools`).
Tool providers are called "sources" in all user-visible text. The backend API routes remain
`/api/v1/mcp/servers` and `/api/v1/mcp/tools` — unchanged. A redirect from `/mcp` to `/tools`
is in place for backward compatibility with any bookmarked URLs.

**Reasoning:** MCP is an implementation detail. Users care about what tools their agents can
use, not which transport protocol delivers them. Surfacing "MCP" in the UI would couple the
user's mental model to a specific protocol, making it harder to add non-MCP tool sources in
the future without a disruptive rename.

**Rejected alternative:** Keeping "MCP Servers" as the UI label. Rejected because it leaks an
implementation detail into the user interface and would require a UI rename when additional
transport types are supported.

**Consequence:** Component directories retain `MCPPage/` names as an internal detail — not
user-facing. Hook names (`useMcpServers`, `useAddMcpServer`, etc.) are unchanged. All
user-visible text uses "Tools" and "source" vocabulary. Backend API routes are not affected.

---

## ADR-031: Native feedback as a Gleipnir runtime primitive

**Status:** Decided
**Date:** 2026-04
**Supersedes (partially):** ADR-007 (sensor/actuator/feedback role model), ADR-008 (two approval modes), ADR-009 (feedback channel resolution)
**Partially superseded by:** ADR-044 (Channel routing model) — the in-app dispatcher implementation detail is superseded; the first-class-feedback principle and `waiting_for_feedback` state machinery are preserved.

**User-facing vocabulary (#656):** This agent-side "feedback" capability and the audience-side "Request" routing (ADR-044) are the **same** operator-visible flow. The UI standardizes on the noun **"Feedback request"** across both surfaces — the agent editor labels the capability "Feedback request", and the audience editor's Request toggle / routing preview ("Feedback requests routed to:") cross-reference it. Internal identifiers (`feedback_request`/`feedback_response` step types, `waiting_for_feedback` state, the `feedback` capability key, the audience `request` field, `RouteToPlugin`/`RouteToInApp`) are intentionally **unchanged** — renaming them is a deferred follow-up.

### Background

The original design (ADR-007, ADR-008, ADR-009) treated feedback as an MCP tool tagged with `capability_role = feedback`. At runtime, Gleipnir would call the external MCP server, which returned a meaningless `{"status": "pending"}` response. The runtime then wrote three audit steps — `tool_call`, `tool_result`, `feedback_request` — for what is conceptually a single action: pausing the run and asking a human. This conflates notification transport (calling an MCP server) with feedback collection (pausing until a human responds).

Approval gating (ADR-029) is already a runtime primitive: the `BoundAgent` intercepts a tool call before MCP dispatch, pauses the run, and waits for a binary approve/deny decision from the operator. Feedback should follow the same architectural pattern, replacing MCP dispatch with a runtime pause that waits for freeform text.

### Decision

**1. Feedback is a runtime primitive, not an MCP concept.**

The runtime injects a synthetic `gleipnir.ask_operator` tool into the agent's tool list at run start (when feedback is enabled). This tool is never dispatched to an MCP server. When the agent calls it, `BoundAgent` intercepts the call — exactly as it intercepts approval-gated tools — pauses the run (`waiting_for_feedback`), and blocks on a `feedbackCh <-chan string` until the operator responds. The agent sees `gleipnir.ask_operator` as a normal tool with a defined input schema; it has no knowledge that the tool is synthetic.

Both agent implementations (`internal/agent/agent.go` and `internal/agent/claudecode/agent.go`) use the `feedbackCh` channel pattern for blocking on operator response. (Note: `internal/agent/claudecode/` was removed in issue #611; only `internal/agent/agent.go` remains.)

**2. MCP tools are always tools.**

The `capability_role` column has been fully removed from the `mcp_tools` table. All MCP tools discovered from external servers are tools without any role distinction. The `CapabilityRole` type, `CapabilityRoleTool`/`CapabilityRoleFeedback` constants, and `MCPTool.CapabilityRole` field have been removed from `internal/model`. A runtime migration (`migrateDropCapabilityRole`) recreates the table without the column for existing databases.

**3. Notification is orthogonal.**

When the runtime creates a feedback request, the `notify` package dispatches outbound alerts (SSE event to the UI, and future Slack/webhook callbacks). The agent does not know about notification transport. This decouples the feedback collection mechanism (synthetic tool + runtime pause) from the notification delivery mechanism (notify package).

**4. Response ingress is pluggable.**

The current ingress channel is the UI (`POST /api/v1/runs/:run_id/feedback`). Future channels — Slack callbacks, email reply parsing — converge to the same API endpoint or an internal resolution interface. The `BoundAgent` blocks on `feedbackCh <-chan string` regardless of which ingress source delivers the response.

**5. Feedback is synchronous from the agent's perspective.**

The run pauses (`waiting_for_feedback`) until the operator responds or the run's context is cancelled. In v1.0 there is no per-feedback timeout: the default is no timeout, consistent with the current `waitForFeedback` implementation. The policy schema supports a `timeout` field (see below) but v1.0 implementation defers enforcement of it. When timeout enforcement is added, expiry resolves the feedback request with a canned "no response received" message and the run continues.

**Note on two modes:** The behavioral distinction between voluntary feedback and policy-gated approval is preserved. What changes is the mechanism, not the concept. Agent-initiated feedback is simply the agent calling `gleipnir.ask_operator`; policy-gated approval remains the runtime interceptor checking `approval: required` flags on MCP tool calls. These are two separate paths through `BoundAgent`.

### `gleipnir.ask_operator` tool contract

The `gleipnir.` prefix signals that this is a runtime-provided tool, not an MCP server tool. Its input schema:

```json
{
  "type": "object",
  "properties": {
    "message": {
      "type": "string",
      "description": "The question or information to present to the human operator."
    }
  },
  "required": ["message"]
}
```

The tool returns a single string: the operator's freeform text response. It appears in the capability snapshot (ADR-018) with no `approval_required` flag and no `presented_schema` narrowing.

### Policy schema change — `capabilities.feedback`

`capabilities.feedback` changes from a list of MCP tool references (the previous `feedback: []` list) to an optional configuration block:

```yaml
capabilities:
  feedback:
    enabled: true   # optional; default: true
    timeout: 30m    # optional; Go duration string; default: no timeout (v1.0 defers enforcement)
```

When `feedback.enabled` is true, or when the block is omitted entirely (which defaults to enabled), the runtime injects `gleipnir.ask_operator` into the agent's tool list. When `feedback.enabled` is false, no feedback tool is available — this supports fully autonomous cron policies that should have no feedback channel.

The `timeout` field, if set, applies a deadline to each individual feedback request, not to the whole run. The schema supports the field now so existing policies do not need to be migrated when timeout enforcement is implemented.

### Reasoning

- The current approach conflates notification transport with feedback collection. The MCP call returns `{"status": "pending"}`, and the run writes three steps (`tool_call`, `tool_result`, `feedback_request`) for what is conceptually one action.
- Approval gating is already a runtime primitive that intercepts before MCP dispatch. Feedback follows the same pattern but collects freeform text rather than a binary decision. Consistency between the two mechanisms reduces implementation surface area.
- Removing the `capability_role` distinction simplifies the MCP registry. Tools are tools. The feedback channel is orthogonal to tool transport.

### Rejected alternatives

- **Keep feedback as an MCP tool with special handling.** Rejected because it conflates transport with collection, produces confusing triple-rendered audit steps, and requires MCP server cooperation (returning `{"status": "pending"}`) for what is entirely a Gleipnir runtime concern.
- **Make feedback a system prompt instruction only.** Rejected per ADR-001 — prompt-based restrictions are not controls. The agent must be able to pause a run deterministically, not just because the system prompt suggests it.
- **Auto-inject feedback into all runs without policy opt-out.** Rejected because some policies (fully autonomous cron jobs) should not expose a feedback channel at all.

### Consequences

- `internal/model`: `CapabilityRole` type and all associated constants (`CapabilityRoleTool`, `CapabilityRoleFeedback`) removed. `GrantedTool.Role` and `MCPTool.CapabilityRole` fields removed. The `CapabilitiesConfig.Feedback` field changes from `[]string` to a `FeedbackConfig` struct (`Enabled bool`, `Timeout duration`).
- `internal/agent`: Both `BoundAgent` implementations inject `gleipnir.ask_operator` as a synthetic tool at run start when feedback is enabled. The `dispatchToolCall` method intercepts calls to `gleipnir.ask_operator` before MCP dispatch, the same way it intercepts approval-gated tools. The existing `waitForFeedback` method is reused with the `message` field from the tool input.
- `internal/mcp`: No changes to the MCP client or registry. Feedback is no longer an MCP concept.
- `internal/policy`: Parser updated to handle the new `capabilities.feedback` config block shape. Prompt generator updated to remove feedback-role tool listing logic.
- `internal/notify`: Remains the outbound notification dispatch point. Receives a `feedback.created` event and fans out to configured channels.
- `schemas/policy.yaml`: Updated to reflect the new `capabilities.feedback` block shape.
- Capability snapshot (ADR-018): `gleipnir.ask_operator` appears in the snapshot tool list with a synthetic marker.
- `internal/db`: The `feedback_requests` table is unchanged — it already stores the feedback lifecycle independently of MCP.
- ADR-007 is partially superseded: the sensor/actuator/feedback three-role model collapses to tools (with optional approval) plus the runtime feedback primitive.
- ADR-008 is partially superseded: "agent-initiated feedback" is no longer a separate mode — it is simply the agent calling `gleipnir.ask_operator`. "Policy-gated approval" is unchanged.
- ADR-009 is superseded in mechanism: the feedback channel resolution (policy-first, system fallback) now applies to the `notify` package configuration rather than to MCP tool selection.

---

## ADR-032: Admin-managed OpenAI-compatible LLM provider instances

**Status:** Proposed (will be marked Accepted when the implementation of spec
`docs/superpowers/specs/2026-04-06-openai-compatible-llm-client-design.md` lands).

**Context.** Gleipnir's existing LLM provider model (ADR-026) supports two
providers — Anthropic and Google — each backed by a vendor SDK and configured
via a single fixed `<provider>_api_key` row in `system_settings`. The provider
list is a static `knownProviders` slice baked in at startup. This does not
extend to adding OpenAI as a third first-class provider (issue #533), letting
operators point Gleipnir at OpenAI-compatible backends (Ollama, vLLM,
OpenRouter, LM Studio, Together, Groq, Azure-via-compat), or allowing
administrators to add or change LLM endpoints at runtime without redeploying.

**Decision.** Introduce a second provider mechanism that coexists with the
existing SDK-backed mechanism:

- **SDK providers (`anthropic`, `google`)** remain exactly as today. One row
  per provider in `system_settings`. Static `knownProviders` slice. Vendor
  SDKs. They are inherently special — vendor-specific features (prompt
  caching, signed thinking blocks, citations, structured outputs) justify
  per-provider client code.
- **OpenAI-compatible provider instances** are admin-managed, persisted in a
  new `openai_compat_providers` table, and registered into the existing
  `ProviderRegistry` at startup and on every admin mutation. Each row is an
  *instance* of one shared client implementation: a single hand-rolled
  `*openai.Client` constructed with the row's `base_url` and decrypted
  `api_key`. The same client serves OpenAI itself
  (`base_url = https://api.openai.com/v1`) and any compatible third-party
  backend.

**Why hand-rolled, not the official OpenAI Go SDK.** OpenAI Chat Completions
is small, stable, and re-implemented by dozens of third-party backends. A
hand-rolled client (~500 lines) permits permissive deserialization that
tolerates compat-backend quirks (omitted fields, slightly different streaming
chunks, missing `/models`). A strict typed SDK would reject responses a
permissive client accepts. Maintaining one client for both OpenAI proper and
compat backends avoids the drift and bug surface of two parallel
implementations of the same protocol. The SDK's value is concentrated in
non-Chat-Completions surfaces (Realtime, Assistants, Responses) that
Gleipnir does not need.

**Why Chat Completions only, not the Responses API.** The Responses API is
OpenAI-only (compat backends do not implement it). Surfacing reasoning
content from o-series models requires it; we accept that reasoning content
is hidden and only reasoning token counts are recorded, via
`TokenUsage.ThinkingTokens`. Standard chat models have no hidden reasoning,
so nothing is lost there.

**Why two mechanisms instead of unifying everything in one table.**
Migrating Anthropic and Google into the new table was rejected because they
are legitimately special: vendor SDKs with features that don't fit a uniform
shape. The two-mechanism approach is honest about the underlying difference.

**Why the reserved-name rule.** The names `anthropic` and `google` are
reserved at the API layer. Without this, an admin could create an
`openai_compat_providers` row named `anthropic` and silently shadow the
SDK-backed Anthropic provider in the registry.

**Why API keys are encrypted at rest.** Reuses the existing
`internal/admin/crypto.go` and `GLEIPNIR_SECRET_KEY` infrastructure already
used for Anthropic and Google keys. No new key-management story.

**Why deletion is destructive without policy checks.** A policy referencing
an unknown provider already fails at run-start with a clear error. A
"references" check can be added later without changing this ADR. In-flight
runs that already hold a client reference complete their current API call
and only fail when their next run starts and tries to look up the provider
in the registry.

**Why connection-test-on-save (with a 404 escape hatch).** Surfacing bad
config to the admin at save time — rather than to a policy author hours
later in a failed run — is the better operator experience. The 404 escape
hatch exists because some compat backends do not implement `/v1/models`;
they should still be usable, with the trade-off that model-name autocomplete
is unavailable for those instances.

**Consequences.**

- New table `openai_compat_providers`. Migration is additive.
- New admin endpoints under `/api/v1/admin/openai-providers`, admin-role gated.
- New section on the existing admin LLM Providers page. Anthropic and Google
  sections unchanged.
- New Go package `internal/llm/openai`, mirroring `internal/llm/anthropic`
  and `internal/llm/google`.
- Policy YAML unchanged. Policies continue to say `provider: <name>`.
- Two parallel provider mechanisms exist after this change. Future LLM
  providers that also speak OpenAI Chat Completions require zero new code
  (just an admin-created instance). Future LLM providers that need a vendor
  SDK require a new package alongside `anthropic` and `google` and an entry
  in `knownProviders`.

**Supersedes / amends.** Builds on ADR-026 (Model-Agnostic Design); does not
supersede it. Adds a second registration mechanism alongside the existing
static one. ADR-001 (hard capability enforcement) is unchanged — the new
client never sees policy details; it only receives filtered tool lists.

**Superseded in part by ADR-033.** The hand-rolled Chat Completions client
described here was renamed to `internal/llm/openaicompat` and is now used
exclusively for admin-managed third-party backends. OpenAI's own API now uses
the official `openai-go` SDK targeting the Responses API (`internal/llm/openai`).
The reserved-name list was extended with `"openai"` to prevent compat rows from
shadowing the premium provider.

---

## ADR-033: Premium OpenAI client split from OpenAI-compatible client

**Status:** Accepted
**Date:** 2026-04

**Context.** ADR-032 introduced a single hand-rolled Chat Completions client
(`internal/llm/openai`) serving both OpenAI's own API and any OpenAI-compatible
third-party backend. This provided compat tolerance but left OpenAI as a
second-class provider — unlike Anthropic and Google, it had no built-in startup
registration and no access to OpenAI-specific features (Responses API, reasoning
tokens, structured outputs).

**Decision.** Split the single role into two:

- **`internal/llm/openai`** — a premium OpenAI client using the official
  `github.com/openai/openai-go` SDK targeting the **Responses API**. Registered
  at startup from the `openai` entry in `knownProviders`, exactly like Anthropic
  and Google. The API key is stored in `system_settings` via the existing admin
  key-management flow.
- **`internal/llm/openaicompat`** — the renamed hand-rolled Chat Completions
  client, used exclusively by `LoadAndRegister` for admin-managed compat rows
  (Ollama, vLLM, OpenRouter, etc.). No behavioral change to the compat path.

**Why now.** The Responses API provides first-class reasoning tokens
(`OutputTokensDetails.ReasoningTokens`), a typed output surface, and reasoning
summary blocks — capabilities unavailable via Chat Completions. The symmetry
with Anthropic and Google (three premium SDK clients + one generic compat
loader) makes the provider model immediately readable.

**Why the Responses API, not Chat Completions.** The Responses API is OpenAI's
current-generation interface. It exposes reasoning items natively, handles
multi-turn state cleanly via the input list, and surfaces per-turn token usage
including reasoning tokens. Chat Completions does not expose reasoning content.
The compat client's Chat Completions path remains available for backends that
need it (compat backends do not implement the Responses API).

**Why reserve `"openai"` at the admin layer.** Without this, an admin could
create a compat row named `"openai"` and, depending on load order, shadow the
premium provider in the registry. The premium providers are registered first;
the compat loader runs after. The reserved-name check makes the invariant
explicit and prevents the ambiguity entirely.

**Consequences.**

- `internal/llm/openai` — new package, `openai-go` SDK, Responses API.
- `internal/llm/openaicompat` — renamed from `internal/llm/openai`. Hand-rolled
  Chat Completions. Compat behavior unchanged.
- `main.go` — `"openai"` added to `knownProviders`; `case "openai"` added to
  the provider-construction switch.
- `internal/admin/openai_compat_handler.go` — `"openai"` added to `reservedNames`.
- `OPENAI_API_KEY` is the env variable whose presence at startup is warned about
  (matches Anthropic/Google pattern — keys are managed through the admin UI).

**Builds on.** ADR-026 (model-agnostic design), ADR-032 (OpenAI-compat loader).

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
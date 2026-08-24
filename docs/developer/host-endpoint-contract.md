# The host endpoint — MCP contract

**Spec:** `mcp-realignment-spec.md` §8 · **ADR:** ADR-057 (amended 2026-08-04) · **Status:** built, not live (M17, #875–#881)
**Code:** `internal/plugin/hostendpoint/`

This document is the normative contract for the host endpoint — the host-side
MCP server managed plugins call for server→host callbacks. It describes what
shipped, not what was planned; where the spec and the code disagree in detail,
the code wins and this document says so.

---

## 1. What it replaces

Under the v1.1 substrate (`internal/plugin/hostsvc`), server→host callbacks
ride a gRPC plane: `ToolService`/`ChannelService`/`TriggerService` in one
direction, and a Host RPC server in the other, each protocol shaped by its own
generated stubs. Amendment 1's organizing principle — **if the host initiates
it, it is not a tool** — pushed host-initiated interaction onto `io.gleipnir/*`
extensions (`docs/developer/extension-io-gleipnir-channel.md` for one of them)
and left one shape for everything else: **plugin-initiated requests to a host
service ride an ordinary MCP server, the host endpoint.**

The payoff is symmetry. A plugin **serves** MCP (tools, events, channel) and
**consumes** MCP (host callbacks) — one protocol dependency, in any language,
covers both directions. gRPC and protobuf leave the system entirely with the
cutover: `hostwire`, the generated stubs, the `buf` toolchain, and the
`Proto gen drift` CI lane all go.

`hostsvc`'s gRPC plane **stays live** until that cutover (#883) removes it.
Nothing here is a dual-write: the host endpoint is a parallel implementation
that main.go does not yet start, built ahead of the switch so the switch is a
single flip rather than a design-while-migrating exercise. The status note in
§9 says exactly how far "built" currently reaches.

## 2. Transport

The host endpoint speaks the stateless 2026-07-28 MCP profile exclusively —
`server/discover`, `tools/list`, `tools/call`, and nothing else. There is no
session and no `initialize`: a legacy handshake gets JSON-RPC method-not-found
(`-32601`), because every plugin that can reach this listener ships against
the realignment contract. Bilingualism belongs to `internal/mcp`'s client,
which has to tolerate arbitrary external servers; a caller of the host
endpoint by definition does not, and tolerating a legacy dialect here would
only mask a broken one.

### 2.1 Header and `_meta` validation (A4 / A1)

The host endpoint enforces the same two request-validation regimes
`internal/mcp`'s `FakeMCPServer` pins for the client side of this transport —
that fake is the in-repo oracle for the wire shape, and the host endpoint's
shapes deliberately match it rather than reinventing an equivalent check:

- **A4 (headers).** `MCP-Protocol-Version` and `Mcp-Method` are required on
  every request and must match the JSON-RPC body (`Mcp-Method` against
  `method`, the protocol-version header against the `_meta.protocolVersion`
  body field on `server/discover`). `Mcp-Name` is additionally required on
  `tools/call` and must equal the tool being called; it does not apply to
  `server/discover`, and a superfluous one there has no spec-defined rejection
  and is ignored.
- **A1 (`_meta` body fields).** `server/discover`'s params must carry
  `_meta.protocolVersion` and `_meta.clientCapabilities`. A missing field is
  `-32602` (Invalid Params) — a client bug — deliberately distinct from the
  header-mismatch code so a caller cannot misread a malformed body as a
  version-negotiation failure. Tool calls carry no required `_meta` body
  fields; A1 is `server/discover`-only.

### 2.2 JSON-RPC error codes vs. `isError` tool results

Two different failure vocabularies exist on purpose, and the boundary between
them is the same one the standard `tools/call` contract draws:

| Code | Meaning |
|---|---|
| `-32020` (`ErrCodeHeaderMismatch`) | A4 header missing or inconsistent with the body |
| `-32022` (`ErrCodeUnsupportedProtocolVersion`) | `server/discover` requested a version other than `2026-07-28` |
| `-32602` (`ErrCodeInvalidParams`) | A1 `_meta` field missing, or `tools/call` params malformed / unknown tool name |
| `-32601` (`ErrCodeMethodNotFound`) | Any method outside `server/discover` / `tools/list` / `tools/call`, including legacy `initialize` |

Everything above is a **transport fault**: the call never reached a handler,
so nothing was authorized, gated, or attempted. A handler-level refusal —
wrong Tier-2 capability, an unmapped actor, a malformed argument the handler
itself rejects — is instead a normal JSON-RPC **result** with `isError: true`
and a stable machine-readable code string in the content
(`cardinality_cap_exceeded`, `permission_denied`, `unauthorized_request_id`,
…), reusing hostsvc's existing code vocabulary so a plugin migrating from the
gRPC plane matches on the same identifiers.

The distinction is not cosmetic. An agent-side (or SDK-side) caller needs to
tell "the call never happened" from "the tool ran and refused" — the former is
retryable after fixing the request shape, the latter is a decision the handler
made about this specific call. Collapsing both into JSON-RPC errors would lose
that distinction; collapsing both into `isError` results would let a caller
retry a header bug indefinitely without ever finding out it was malformed.

## 3. Auth

Every request carries the per-generation instance token as a bearer
`Authorization` header — unchanged in substance from the gRPC plane's
`gleipnir-instance-token` metadata key, with the existing interceptor logic
ported to HTTP middleware rather than redesigned (§4).

### 3.1 Two resolver eras

`TokenResolver` is one interface with two implementations, and the middleware
does not care which is behind it:

- **`RegistryResolver`** authenticates against the v1.1 in-memory
  `identity.Registry` — the same registry hostsvc's gRPC interceptor
  consults, so a subprocess-era plugin's `GLEIPNIR_INSTANCE_TOKEN`
  authenticates identically on either transport. The registry is per-instance
  (`Issue` auto-revokes the prior token on reissue), so `Identity.Generation`
  is always `0` — the concept does not exist at this layer.
- **`GenerationTokenResolver`** authenticates against the container
  substrate's per-generation token rows: it hashes the presented token the
  same way rotation stored it (`reconciler.HashInstanceToken` — two
  implementations of "the stored form" is one more than the number that can
  be right) and looks the hash up via `GetContainerGenerationByTokenHash`.

This is what lets the host endpoint come up against the v1.1 registry today
and switch to generation rows once the reconciler goes live, without the auth
semantics — or the middleware — moving.

### 3.2 A DRAINING generation still authenticates

`GenerationTokenResolver`'s query excludes only **revoked** tokens, not
draining ones. This is deliberate: tokens are revoked at **retire**, not at
**switch**. Revoking a generation's token the moment a rotation switches
traffic to the new one would fail every in-flight call the old generation is
still draining — exactly the work the drain step exists to protect. A
retired generation's token is revoked and fails authentication with no
separate revocation check to remember here; the query itself is the
enforcement point.

### 3.3 Middleware order: token → generation → call-id

`Chain` composes the three ported interceptors in the same order hostsvc
chained them on gRPC:

1. **`RequireInstanceToken`** resolves the bearer token and rejects `401`
   before the handler runs on either a missing header or an unknown/revoked
   token — kept as two distinct messages, since a plugin author debugging
   "missing" (the SDK never attached it) is in a different bug than one
   debugging "unknown or revoked" (rotation retired their generation). A
   resolver-internal failure (a DB fault) is `500`, not `401` — telling a
   healthy plugin its *token* is bad would send an operator debugging
   credential delivery instead of the host.
2. **`RequireGenerationSlot`** acquires a `generation.Controller` slot for the
   resolved instance before the handler runs and releases it after, so a
   rotation can drain in-flight host-endpoint calls exactly as it drained gRPC
   ones: an in-flight call continues under a wrapped cancellable context and
   is force-cancelled only after the drain grace period; a **new** call
   arriving mid-drain blocks in `Acquire` and turns into `503` on its own
   request deadline rather than the drain's. An unregistered instance also
   rejects `503` — "no generation state" means the reconciler has not (or no
   longer) declared this instance, so traffic for it has nowhere valid to go.
   A request that reaches this stage with no identity in context (the
   previous stage never ran, or was skipped) passes through untouched, so a
   mis-composed chain surfaces the auth layer's own error instead of masking
   it behind a generation failure.
3. **`WithCallID`** attaches an unambiguous `Gleipnir-Call-Id` header value to
   the context and never rejects on its own — a missing or ambiguous
   (multi-valued) header just means the handler sees `("", false)`, and any
   call-scope enforcement belongs to the handler that needs it (`get_run_context`,
   §5).

`hostsvc.RejectIfDetached` is deliberately **not** ported. It existed for
future call-scope-bound RPCs, and the one RPC that ever flirted with call
scope — `WriteAuditStep` — authenticates by request-ownership instead and is
removed from the §8 inventory entirely (§5, §7). Carrying an unused guard
across a transport rewrite preserves the code and loses the reason; if a
call-scope-bound tool ever appears, build the guard against that tool's
actual contract.

## 4. The host-plane invariant

> **Host-plane invariant (normative).** The host endpoint is host-plane only:
> its tools are never registered in `internal/toolregistry`, never appear in
> any discovery a policy can reach, and are never grantable; the listener
> binds exclusively to the per-instance internal networks, as a separate
> listener from the operator API.

Under gRPC this separation was automatic — the two directions spoke different
protocols, so a host tool could not physically end up in the tool registry a
policy grants against. **Both directions now speak MCP.** That automatic
separation is gone, and nothing about the wire format tells `tools/list`
output from `host/…` tool definitions apart — they are the same JSON shape on
the same kind of server. The rule that used to be structural is now a
deliberate topology-and-registration discipline that a future change could
violate by accident: a refactor that folds host-endpoint tool registration
into the same code path as plugin-endpoint tool registration would be one
line away from making every host tool grantable to an agent, which is an
ADR-001 capability-enforcement break.

That is why the invariant is **asserted, not assumed**. Two independent
mechanisms enforce it:

- **`AssertHostPlane`** runs at startup and refuses to boot a process whose
  shared tool-namespace registry (`internal/toolregistry`) holds a
  host-endpoint tool name — either the exact name (`host/log`) or a dot-name
  whose tool part matches one (`slack.host/log`, which would mean a source
  is offering a host tool for granting). It is a boot-time check rather than
  a logged warning on the same posture #871 established for the policy
  service: a check that nothing asserts is a check that silently stops
  running, and this one guards the capability boundary itself.
- **`ListenerSet.Add`** refuses a wildcard bind address (`0.0.0.0`, an empty
  host, or any address `net.IP.IsUnspecified` reports) with `ErrWildcardAddr`.
  "Internal networks only" must not depend on a caller passing the right
  address every time; a wildcard bind would expose every host tool on every
  interface the host has, including the operator API's.

### 4.1 Why the `host/` prefix makes the assertion exact

`ToolNamePrefix = "host/"` is what turns `AssertHostPlane` from a heuristic
into an exact match. A `/` never appears in the `<source>.<tool>` dot-names
the shared registry otherwise holds — it is the extension-method separator
(`channel/notify`, `events/listen`), not a character a tool name legitimately
contains — so a `host/…` name showing up in the registry is unambiguously a
leak, with no false positives to tune around. A registry entry that merely
*contains* the substring "host" (`slack.hostname_lookup`) is not a match:
the comparison is against the full tool part after the last `.`, not a
substring search.

The full §8 tool inventory (`ToolNames()`) is declared in `tools.go` **ahead
of** any handler implementation, specifically so the boot-time assertion
guards the complete set from the very first commit that carries this package
— a name added to the inventory later would be a name the assertion never
guarded during the window before it existed.

## 5. The boundary rule

> **Boundary rule (normative, unchanged in substance).** Request/response
> addressed to a host service → host endpoint. Fire-and-forget facts for the
> trigger pipeline → events extension. The rule is about direction and
> addressing, not wire format. An identity proof must never be able to launch
> a policy; an event never needs a response.

Both the host endpoint and `io.gleipnir/events` speak MCP now, which is
exactly the condition under which the two could quietly blur together if the
distinction were left to convention. The rule survives the transport
unification because it was never about the wire format in the first place —
gRPC vs. MCP was incidental, direction and addressing are the substance.

A plugin-initiated **question that expects an answer and names who should
answer it** — "is this actor allowed to approve this?", "give me this run's
context", "record this metric" — goes to the host endpoint as a `tools/call`.
A host-observed **fact that a trigger might react to** — a Slack message
landed, a webhook fired — goes out through `io.gleipnir/events`, which is
stream-first and carries no response channel at all. The asymmetry in the
issue's own phrasing is the point: **an identity proof must never be able to
launch a policy** (so `SubmitIdentityProof` is a host-endpoint tool call, not
an event a trigger could bind against), **and an event never needs a
response** (so nothing on the events side blocks waiting for one). Each half
of that sentence rules out folding the two systems together in either
direction.

## 6. Method inventory (as shipped)

Every kept method is unary; nothing on the host endpoint streams. The only
long-lived stream in the realigned system is `events/listen`, served by the
**plugin's** endpoint with the host as the client — the host endpoint itself
never holds a connection open.

`WriteAuditStep` and `EmitEvent` are removed from the inventory: the
`feedback_response` audit path `WriteAuditStep` carried is subsumed by task
completion (§7's `AuthorizeActor` replaces the piece of it that mattered for
authorization), and `EmitEvent` is subsumed by `events/listen`. Both names are
absent from `ToolNames()` on purpose. Their retirement sequencing against the
still-live v1.1 Slack plugin was #880's open decision, split in two: the
`WriteAuditStep` half landed in #894, and #906 landed the structural half of
the `EmitEvent` side — `hostsvc.EmitEvent` refuses a v2 event-source caller
today, but the gRPC method itself is not deleted until milestone #22, once
the Slack plugin is rewritten against `events/listen` (#19) and this host
endpoint is actually reachable.

### `host/get_instance_config`

Returns the calling instance's `config_json` verbatim. No arguments, no audit
event — reads are logged at `Debug` only.

### `host/get_credentials`

Returns the calling instance's decrypted credentials JSON
(`{"credentials_json": "..."}`), or an empty string when no credentials are
configured (a valid state, not an error). No caching: the DB is hit on every
call per the spec's pull-only credential model. No audit event; credential
*mutations* are audited by the admin credential lifecycle code, this is only
a read.

This method exists — rather than being fully subsumed by the egress proxy's
header injection — because **header injection cannot cover streams**: a
plugin holding a long-lived substrate connection (a Slack Socket Mode
websocket, an IMAP session) needs the standing credential itself, not a
header the host attaches to each individual outbound request.

### `host/get_run_context`

Args: none beyond the `Gleipnir-Call-Id` header, which `get_run_context`
requires (`failed_precondition` when absent, or when the call id is not
currently in flight). Result: `{run_id, policy_id, started_at, step_index}`.

**Load-bearing rule: the ownership check.** The call id resolves to an
in-flight call's `(run_id, policy_id, instance_name)`; if the instance name on
that call does not match the authenticated caller's own instance name, the
call is refused with `permission_denied` / `unauthorized_request_id` **and**
a high-severity `unauthorized_request_id` audit event is written. Without this
check, any authenticated plugin could present a foreign call id and read
another instance's run context — the check exists precisely to make that
impossible, not merely inconvenient.

### `host/emit_metric`

Args: `{name, value, labels}`. Result: `{ok: true}`.

Runs through the shared ADR-047 guard in `internal/plugin/pluginmetrics`
(the same guard the gRPC plane uses, until #883 deletes that plane): forced
`gleipnir_plugin_` prefix, auto-injected `plugin`/`instance` labels, a
100-distinct-value cardinality cap per `(metric, label-key)` with loud
rejection rather than an unbounded `GaugeVec`, and rejection of an
inconsistent label-key set across calls for the same metric name.

### `host/log`

Args: `{level, msg, attrs}`. Result: `{ok: true}`.

Caps carried over from hostsvc unchanged: `msg` ≤ 4 KiB, `attrs` ≤ 32 entries,
each key/value ≤ 256 bytes. Run correlation (`run_id`, `policy_id`,
`step_index`, `call_id`) attaches automatically when the request's
`Gleipnir-Call-Id` resolves to an in-flight call; otherwise the record carries
only `plugin`/`instance`. This preserves ADR-047's
structured-host-RPC-not-stdout rationale across the transport change: plugin
logs still ride a host RPC rather than stdout, because stdout gives no
ordering or attribution guarantee tying a line to the work that produced it.

### `host/set_health_state`

Args: `{profile, capability, state, detail}`. Result: `{ok: true, applied: bool}`.

This is the one method whose behavior deliberately changed rather than
ported unchanged: health is now **per-capability**, not per-instance (#814).
Under the v1.1 RPC, one missing OAuth scope on one capability marked the
*whole instance* unhealthy, silently taking every other capability the
instance served perfectly well out of routing. `set_health_state` now records
into `caphealth.Registry.SelfReportCapability`, keyed by `(profile, capability
name)`, which narrows routing for exactly that surface and nothing else.

The state a plugin may claim is deliberately restricted to `healthy` and
`unhealthy` — nothing else in the model's health vocabulary
(`circuit_broken`, the `pending_*` family) is something a plugin may assert
about itself; those are host- or admin-observed verdicts. And per the §8.1
"worsen-only" rule, an *improvement* report is a no-op — `{ok: true, applied:
false}` — never a write: recovery is the host's own observation to make,
because a plugin that could self-clear a fault could just as easily mask one.

### `host/run_history_read` (Tier-2)

Args: `{policy_id?, limit?}`. Result: `{runs: [{run_id, policy_id, status,
started_at, finished_at}, ...]}`.

Gated by the manifest's `tier2_capabilities` declaration, parsed fresh on
every call (no caching), so a hot-reloaded manifest that drops the capability
takes effect on the very next call. An undeclared capability writes a
high-severity `unauthorized_tier2_call` audit event and returns
`permission_denied` — the audit event is the point of the gate: an operator
needs to learn a plugin *reached* for something it was never granted, not
just that the individual call failed silently.

**Scoping, the part that must survive the port unchanged:** results are
restricted to policies that reference the calling instance, either via a tool
grant (`capabilities.tools` contains an entry with the `<instance>.` dot-name
prefix) or via a subscribed trigger bound to the instance as its source. An
out-of-scope `policy_id` in the request returns an **empty** list, never an
error — the response must not leak whether the requested policy exists at
all.

### `host/user_directory_read` (Tier-2)

Args: `{role_filter?}`. Result: `{users: [{user_id, username, role}, ...]}`.

Same Tier-2 capability gate and audit event as `run_history_read`. The
response shape is pinned to exactly three fields — no credentials, no session
data — by a dedicated struct and an exact-key-set test, so adding a field is
a deliberate edit here rather than an accident of forwarding a new column
from upstream.

### `host/authorize_actor`

Args: `{request_id, actor_external_id}`. Result: `{authorized: bool,
user_id?: string}`.

Replaces the piece of v1.1's `WriteAuditStep` that resolved an
externally-asserted actor identity (a Slack `user.id` or similar) against
Gleipnir's role model. That RPC bolted the check onto a write-then-refuse
call: the actor id rode along on the `feedback_response`, and the host
resolved-then-either-completed-or-rejected *after the fact*. The realigned
flow inverts this to a **pre-check**: a plugin calls `authorize_actor` first,
and only a caller that gets `authorized: true` back goes on to complete its
own Tasks-extension task with `{option_id, actor_external_id}`. The role gate
itself is unchanged — resolution requires a Gleipnir user with role
`approver`, `operator`, or `admin` (`auditor` is deliberately excluded: an
auditor's job is to observe the record, not to become part of it).

**Resolution is verified-identity-only.** `ActorDirectory` is the seam:
today's only implementation, `DBActorDirectory`, resolves against the
admin-managed `users.slack_user_id` mapping — an admin-set link, which is
exactly the "admin-set" half of ADR-058's bar for what may feed actor
authorization. Milestone #18's verified `plugin_user_identities` link table
widens what backs this interface without this handler changing at all.

**An unauthorized actor is a non-error result, audited.** `found=false` (no
mapping) and a mapped user with none of the three roles are treated
identically: the call still succeeds at the transport level, the result
carries `{"authorized": false}`, a high-severity
`unauthorized_approval_attempt` audit event is written, and — critically —
**nothing is resolved**. The underlying request stays open for a
legitimately authorized actor, or the same actor corrected, to try again.

**The poll-now hint is best-effort latency, never correctness.** On a
successful authorization, `authorize_actor` calls an optional `PollHint`
(structurally satisfied by `*mcp.PollScheduler.PollNow`, declared here as an
interface rather than an import so this file's dependency footprint stays
exactly the DB + model surface the handler needs) to close the ADR-055
resolution-latency gap to the old in-memory gRPC waiter. A nil `PollHint`
still authorizes correctly — it just resolves at the ordinary poll cadence
instead of immediately. A poll-hint failure is logged and swallowed: it never
turns a correct authorization into a refusal, because failing the hint would
convert a latency regression into a correctness one.

### `host/submit_identity_proof`

Args: `{external_user_id, code}`. Result: `BindResult` — see §7.1.

The plugin's leg of the `inbound_code` identity-link flow (spec §9.1): a user
sends a code through the medium, the plugin relays it here, the host binds it
against a pending link it is already tracking. `PendingLinkBinder` is
milestone #18's implementation seam for the host-owned pending-link state
machine (code generation and expiry, entirely host-side); a nil `Binder`
means no link flow is configured, and every proof rejects with
`no_pending_link` — treated identically to "no pending link found", since
from the plugin's side those are the same outcome. The handler never inspects
or reshapes what the binder decided; it forwards the result verbatim, which
is what keeps §7.1's disqualifying property true regardless of how #18
implements the binder.

### `host/get_user_config`

Args: `{external_user_id}`. Result: `{user_config_json: string}`.

Reads one external user's per-plugin config (spec §9.2) via the `#18` seam
`UserConfigReader`; a nil reader returns `"{}"`, not an error — a plugin
asking about a user with no preferences set yet needs a usable default, not a
fault. No audit event, same posture as `get_instance_config`: this is a
presentation-preference read, not a security-relevant action. Two ADR-058
invariants hold regardless of what backs the reader: **user config may never
grant capability** (role gates and policy grants are entirely untouched by
anything this method returns), and any routing-affecting preference in that
config uses the channel-neutral `delivery: direct | shared` vocabulary — never
a medium-specific word like "DM" — interpreted only by the **host's** audience
dispatcher; a plugin reads its own user config for its own presentation
concerns only.

## 7. Two wire-vocabulary decisions this document settles

Both of these were flagged during implementation as needing one canonical
answer rather than being re-decided by whichever consumer reaches them next.
This is that answer.

### 7.1 `submit_identity_proof`'s result vocabulary is `{accepted, reason}` — and nothing else

`BindResult` carries exactly two fields: `accepted` (bool) and `reason`
(string, only on rejection — `no_pending_link` today). It deliberately does
**not** carry `user_id`, `role`, or an echoed `external_user_id`.

The reason is ADR-058's disqualifying property: an unverified, self-asserted
identity must never be able to resolve a permission request by riding *this
method's response* into actor authorization. If `submit_identity_proof`
echoed back a `user_id`, a caller downstream could be tempted to treat that
echo as proof of linkage — but the linkage the host actually trusts is the
**durable link record**, written by the binder *before* it returns
`Accepted: true`, not anything visible in this call's own result. Keeping the
result vocabulary minimal makes that structural rather than a convention a
future caller has to remember: there is nothing in the response worth
misusing, because nothing identity-bearing is in it.

### 7.2 `authorize_actor`'s `request_id` is an opaque, caller-supplied identifier — mapping ownership is the host's, not this method's

`request_id` identifies the pending ask to the **caller** — the plugin's own
Tasks-extension task id, the one value it holds at click time. This method
forwards it **verbatim** to `PollHint.PollNow`; it does not resolve, validate,
or translate it into a concrete `mcp_tasks` row.

The mapping from a plugin-side task id to an `mcp_tasks` row is owned by the
host's wiring — the concrete `PollHint` implementation wired in at
construction time, not yet built — and the hint's semantics are **best-effort
latency, never correctness** (§6, `host/authorize_actor`). A `PollHint` that
cannot resolve `request_id` to anything fails quietly and the underlying
request still resolves at the next scheduled poll tick.

This is stated as contract, not merely as current behavior, so #19's Slack
rewrite and the milestone-20 conformance suite have one answer to build
against rather than each inferring their own from the implementation: a
conforming `request_id` is opaque to `authorize_actor` itself, and any
correctness the caller needs from the identifier must come from elsewhere in
the flow (the task id itself, or a durable record keyed by it), never from
this method having resolved or validated it.

## 8. Versioning

The host endpoint declares its own version through its own `server/discover`
— `_meta.serverInfo.name = "gleipnir-host-endpoint"` (the `ServerName`
constant), `_meta.serverInfo.version` = the `Version` constant (currently
`0.1.0`, since the milestone is still in flight). This mirrors
`docs/developer/managed-mcp-endpoints.md`'s equivalent claim for managed MCP
servers: ADR-042's per-service SemVer discipline maps onto **two** endpoints
now (the plugin endpoint and the host endpoint) in place of the proto package
versions gRPC used to carry. Contract enforcement moves accordingly, from
`buf lint` structural checks to the milestone-20 conformance suite — which
has the advantage of covering every plugin language, not just Go.

Modern-only, as stated in §2: there is no legacy `initialize` handshake to
version-negotiate down to, because every caller that can reach this listener
already speaks the realignment contract. A legacy handshake attempt is
`-32601` method-not-found, not a dialect the endpoint tries to accommodate.

## 9. Status: built, not live

Everything described above is real, tested code — it is not what shipped in
main.go's boot path. Today, the *only* piece of this contract that actually
runs is `AssertHostPlane`, called once at startup to guard the host-plane
invariant (§4) against whatever the v1.1 plugin runtime has registered. No
`ListenerSet` is constructed by `main.go`, no instance network carries this
traffic, and no plugin calls any of these methods over MCP yet — the v1.1
gRPC plane in `internal/plugin/hostsvc` is what plugins actually call today.

The `ListenerSet` is reconciler-driven: once the container substrate goes
live (the reconciler is merged but not started by `main.go` — see the root
`CLAUDE.md`'s settled-decisions entry for ADR-053…ADR-060), the reconciler
calls `Add`/`Remove` as per-instance networks come up and go down, exactly as
it already does for containers and subnets. Until then, this document
describes a fully-specified, fully-tested destination that the substrate has
not yet been switched onto.

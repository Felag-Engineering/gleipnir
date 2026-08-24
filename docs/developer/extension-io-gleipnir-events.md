# `io.gleipnir/events` — MCP extension contract

**Version:** 1.0.0 · **Status:** implemented (negotiation + `events/discover` host client; `events/listen` is normative but unimplemented — see §7) · **Spec:** `mcp-realignment-spec.md` §4, §5 · **ADR:** ADR-054

This document is the normative contract. Per spec §4.2 discipline, no vendor
appears in normative text; concrete event sources appear only in the
[examples appendix](#appendix-worked-examples).

---

## 1. Why this is an extension and not a tool

Amendment 1 states the rule this contract exists to satisfy: **host-initiated ⇒
not a tool.**

The host decides whether it is listening for events at all, and a policy binds
to an event kind — the model is never in that loop and must never be able to
join it. If event delivery were reachable through `tools/call` it would be a
grantable capability, and there is no version of "an agent can trigger its own
future runs" that is safe to grant.

The same reasoning produced `io.gleipnir/channel` (`extension-io-gleipnir-channel.md`
§1). The two extensions are siblings, and the channel doc's framing is the
clearest way to say it: one carries messages **out** to a human, the other
carries signals **in** to the host. Neither is ever addressable by a model.

## 2. Design constraint: coin no vocabulary

CloudEvents 1.0 supplies the envelope. JSON-RPC supplies the framing. The
**only** Gleipnir-coined names in this entire contract are:

- two method names — `events/discover`, `events/listen`;
- one notification method name — `events/event`;
- one CloudEvents extension attribute — `gleipnirseq`.

Everything else — the envelope's `specversion`, `source`, `type`, `id`,
`time`, `data`; the JSON-RPC request/response/notification shapes — is
adopted, not invented. This is a constraint, not a coincidence: adopting
CloudEvents keeps the eventual §14 WG proposal readable as "an MCP transport
binding for CloudEvents" rather than a novel envelope, and every field this
contract does not coin is a field nobody has to relearn.

## 3. Versioning and steward obligations

SemVer from birth, with the same deprecation discipline Gleipnir expects of
MCP itself (spec §5: "the moment a third party implements it, Gleipnir is a
spec steward with the same obligations we expect of MCP itself").

| Change | Meaning |
|---|---|
| **Patch** | Clarifications; no wire change. |
| **Minor** | Additive only: new optional fields, new enum members a conforming implementation may ignore. |
| **Major** | Anything a v1 implementation would misread. Two-major-version deprecation window before removal, matching ADR-042's plugin-service policy. |

The current version is `1.0.0`, declared by both sides in the capability
entry.

## 4. Negotiation

The extension is negotiated through the standard `extensions` capability map,
in the **`server/discover`** result — the modern handshake, since the
2026-07-28 transport is stateless and never runs `initialize`.
`server/discover` is the only place a modern server declares anything.

```jsonc
// server/discover result
{
  "supportedVersions": ["2026-07-28"],
  "capabilities": {
    "extensions": {
      "io.gleipnir/events": {
        "version": "1.0.0",
        "heartbeatMs": 15000,
        "maxBatch": 25
      }
    }
  }
}
```

**Both methods require the 2026-07-28 transport.** A legacy-pinned client
refuses to issue them, for the same reason `io.gleipnir/channel` does: the
transport a legacy handshake produces has no session that could possibly
understand a 2026-07-28 extension.

**Negotiation happens in the handshake, never by attempting a method and
observing the failure.** A host that probed `events/discover` to find out
whether a server supported it would be exactly the anti-pattern the channel
doc names for `channel/notify`: attempting delivery to find out whether
delivery is possible. `events/discover` has no such side effect, but the rule
is the same rule for the same reason — negotiation is a declared fact, not an
inferred one.

### 4.1 Declaration fields

| Field | Type | Meaning |
|---|---|---|
| `version` | string | Contract version the server implements. |
| `heartbeatMs` | integer | The interval, in milliseconds, at which the server commits to emitting an SSE comment frame on an open `events/listen` stream (§7). |
| `maxBatch` | integer, optional | A hint for how many events the server may push in one delivery. Not consumed by the host client shipped with this contract version; see §6.5 in the client-facing package doc for why it is carried anyway. |

**A malformed declaration is not a failed handshake.** It decodes to the zero
value — no usable heartbeat, no event kinds discoverable — which resolves
nothing and never enters model context, for the same fail-closed reason
`io.gleipnir/channel`'s malformed-declaration handling does. A broken events
declaration must never stop a server's *tools* from working.

Three states are distinguishable, and routing needs all three:

| State | Meaning |
|---|---|
| not declared | The server emits no events. Binding a policy to it is a configuration error. |
| declared, readable | Use as declared. |
| declared, unreadable | A broken events plugin. Discovers nothing; worth surfacing as a health fault. |

## 5. The core principle

> **The event as control signal is host-captured and never enters model
> context. Event payloads MAY reach model context only as untrusted data,
> when a policy explicitly templates them into the task prompt.**

This sentence is quoted verbatim from spec §5 and is binding word for word —
it is the control-plane/model-plane separation ADR-046 already encodes for
`run_steps` vs. `plugin_audit_events`, restated here as protocol design. An
event firing a run is a host decision made from data the host captured; it is
not something the model observed, reasoned about, or was ever shown. The only
route an event's payload has into a model's context is the one every other
piece of untrusted data has: a policy author explicitly choosing to interpolate
it into the task prompt, with all the untrusted-content handling that implies.

## 6. `events/discover`

Returns the event kinds a server may emit, with binding schemas and
per-kind guidance. Discovery lives in the protocol; the signed manifest
**attests** which kinds a plugin may emit, and drift between manifest and
discovery is a health fault (spec §5), not a protocol violation to paper
over.

```jsonc
// request
{ "jsonrpc": "2.0", "id": 4, "method": "events/discover", "params": { "_meta": { … } } }

// result
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "kinds": [
      {
        "kind": "issue.opened",
        "guidance": "Fires when an issue is opened.",
        "binding_schema": {
          "type": "object",
          "properties": { "priority": { "type": "string" } }
        },
        "operators": { "priority": ["eq", "in", "gt", "lt"] }
      }
    ]
  }
}
```

| Field | Meaning |
|---|---|
| `kind` | The event-kind identifier. Echoed verbatim as the CloudEvents `type` on every event of this kind (§7). |
| `guidance` | Server-authored prose shown to an operator binding a policy to this kind. Untrusted, server-controlled text — rendered as content, never as instructions, same as every other server-authored string this package handles. |
| `binding_schema` | The JSON Schema for the typed binding filters a policy may set on this kind (ADR-048). Never JSONPath — matching the rest of the subscribed-trigger design. |
| `operators` | The ADR-052 allowed-operator set per binding field: a map from field name to the operator names a policy may use against it. **Carried on the wire, not yet consumed.** ADR-052 decided operator selectability but deferred implementation; shipping the wire shape now means that adoption is a non-breaking minor version bump later, not a wire change. |

Every field above is a server-controlled string or collection and is bounded
on decode (kind names, guidance text, operator names, and the number of kinds
per response all have caps) — the same bounded-untrusted-string discipline
the rest of this package's extensions follow, because a plugin is external
code regardless of how well-signed it is.

## 7. `events/listen`

The long-lived call that actually delivers events. Normative in full here,
even though no client sends it yet (issue #900 builds that): everything a
future implementation needs to conform is decided by this section, not left
for the implementer to improvise.

### 7.1 Shape

`events/listen` is a JSON-RPC **POST whose response is held open** and framed
as Server-Sent Events (`Content-Type: text/event-stream`). This is not a new
transport mode — the 2026-07-28 streamable-HTTP transport already uses SSE as
its server→client streaming mode for exactly this shape of exchange, so
adopting it here is a binding, not an invention (see §7.5).

One JSON-RPC message rides each SSE `data:` frame. Every delivered event is a
JSON-RPC **notification** (no `id`, so it draws no response):

```
event: message
data: {"jsonrpc":"2.0","method":"events/event","params":{ <CloudEvent 1.0 envelope> }}
```

A **clean server close** is a JSON-RPC **response** to the original
`events/listen` request id, not a bare connection drop:

```
event: message
data: {"jsonrpc":"2.0","id":9,"result":{"reason":"...","cursor":"..."}}
```

`reason` is a short, human-readable explanation (a rolling restart, a
graceful redeploy); `cursor` is the ack point the client resumes from on
reconnect (§7.3). A connection that simply drops without this response is not
a clean close and is handled as a dead stream (§7.4), not as a normal end of
subscription.

### 7.2 Request params

```jsonc
{
  "kinds": ["issue.opened", "issue.closed"],
  "scope": { … },
  "cursor": "opaque-string-or-absent"
}
```

| Field | Meaning |
|---|---|
| `kinds` | The event kinds this listener wants. |
| `scope` | Reserved for future narrowing (e.g. a workspace or repository scope); opaque to this contract version. |
| `cursor` | The sequence value to resume after, echoing a prior `gleipnirseq` (§7.3). Absent on a first connection. |

### 7.3 CloudEvents envelope and cursor semantics

Every delivered event is a CloudEvents 1.0 envelope:

| Field | Meaning |
|---|---|
| `specversion` | Always `"1.0"`. |
| `source` | The emitting server's own identifier. |
| `type` | The event kind (`EventKind.Kind` from §6). |
| `id` | The dedup key — consumed downstream by `internal/plugin/dedup` (unchanged by this extension). |
| `time` | Event timestamp. |
| `data` | The payload. Host-captured; §5 governs where it may go from here. |
| `gleipnirseq` | The one Gleipnir-coined CloudEvents extension attribute (§2): a `uint64` sequence number the cursor acks against. Lowercase-alphanumeric, ≤20 characters, per the CloudEvents extension-attribute naming rules — chosen so this contract's one coined field is itself spec-compliant CloudEvents, not an exception to it. |

**There is no in-band ack.** The ack **is** the cursor sent on the next
(re)connect's `cursor` param — nothing on the open stream ever tells the
server "I have this one." The server buffers durably and is responsible for
replaying everything after the acked `gleipnirseq` on reconnect.

**Delivery is at-least-once.** Redelivery is application-level by design —
core MCP removed transport-level resumability, so this extension does not
invent a substitute; `internal/plugin/dedup` is the existing downstream that
makes at-least-once effectively-once, unchanged by this extension landing.

### 7.4 Heartbeat and dead-stream detection

**The server MUST emit an SSE comment frame** (a line beginning `:`, per the
SSE spec — content-free, invisible to `events/event` consumers) at its
declared `heartbeatMs` (§4.1). **The client treats three consecutive missed
heartbeats as a dead stream** and reconnects.

Rationale: a wedged TCP connection — one where packets stop moving but the
socket never signals close — is indistinguishable from a quiet event source
without some positive, timed signal. A heartbeat converts "nothing happened"
into "I confirm nothing happened," and 3× the declared interval before
acting on silence tolerates one missed beat (GC pause, transient network
blip) without either side treating a live, quiet stream as dead.

### 7.5 Alternatives considered

- **Long-poll** is documented as the fallback binding for an environment that
  cannot hold a response open (e.g. certain serverless deployment targets).
  Not implemented by this contract version; noted so a future minor version
  has a named place to add it rather than inventing a second events
  extension.
- **Webhook** is a possible v2 binding for WG alignment, where MCP's
  ecosystem may converge on inbound delivery as a first-class pattern. v1 is
  stream-first because Gleipnir's deployment audience — a homelab-scale host
  behind NAT, no public inbound endpoint by default — makes outbound
  connections; it does not reliably accept inbound ones.
- **SSE** is the version actually specified here, and the reason is
  structural, not aesthetic: it is the MCP 2026-07-28 streamable-HTTP
  transport's own server→client streaming mode. Choosing it is a **binding**
  of an existing transport capability to this extension's needs, not an
  invented mechanism — the same posture §2 takes toward CloudEvents.

## 8. Scope

Initially closed to **signed managed plugins only** — the same scope
`io.gleipnir/channel` has, and for the same reason: believing an
`io.gleipnir/events` declaration from a server an operator merely pasted a URL
into would let that URL feed policy bindings and launch runs, which is not
what registering an external MCP server means.

Structurally extensible to per-server admin opt-in for external MCP servers
later — an explicit decision, not a default, and not yet built. The
trust-tier drop in the host client (§4.1's "declared, unreadable" case's
sibling: "declared, but by a server not entitled to declare it") makes this
concrete: an external server's `io.gleipnir/events` declaration is discarded
at negotiation, with a warning, before anything downstream ever sees it.

Delivery flows into the existing pipeline unchanged: `events/listen` client →
dedup → `GetSubscribedActivePolicies` → binding compile/evaluate →
`RunLauncher.LaunchWithConcurrency`.

## 9. Rule-of-three paper validation (spec §4.2)

A profile contract does not freeze until three deliberately dissimilar cases
walk through it without special-casing. If one needs an exception, **the
profile is wrong, not the example.**

### 9.1 Email — a source that emits nothing

Email has no natural "event" to discover: nothing in a mailbox is a signal a
host should react to without a human first reading it, and treating "mail
arrived" as an event kind would blur exactly the control/model-plane line §5
draws.

**Verdict:** the profile is **omitted cleanly.** An email-delivery plugin
declares `io.gleipnir/channel` (it is squarely a *human channel*, walked
through in that doc's §9.1) and simply does not declare `io.gleipnir/events`.
Nothing in this contract requires a partial or stub implementation, and "not
declared" (§4.1) is a first-class answer, not an error.

### 9.2 A code-forge or home-automation event feed — the primary case

A source with a steady stream of discrete, typed occurrences: an issue
opened, a sensor crossing a threshold, a release published. This is the case
`events/discover` and `events/listen` are built for.

**Verdict:** fits directly. Several `EventKind` entries, each with its own
`binding_schema` (issue priority, sensor id, release channel) and — once
ADR-052 selectability ships — its own `operators` set. High event volume
argues for a real `heartbeatMs` and makes the at-least-once/dedup design load
-bearing rather than theoretical.

### 9.3 A vanilla ecosystem MCP server — tool-only

An off-the-shelf server exposing `tools/list` and `tools/call` and nothing
else.

**Verdict:** **omitted cleanly, and unchanged.** It never sees an events
method, because negotiation happens in the handshake (§4) rather than by
attempting a call and observing the failure. This is also the case that
validates the extension boundary from §1: a tool-only server's tools are
grantable, and its (nonexistent) events surface is not — there is nothing
here for a policy to bind to, and nothing here for a model to reach.

### 9.4 What the exercise changed

Two contract decisions come directly from these three walkthroughs:

1. **Email validated that "no event profile" must be a clean, silent
   omission**, not a required stub — the same lesson `io.gleipnir/channel`'s
   §9.2 (an event-only source) drew in the opposite direction.
2. **The vanilla-server case is why negotiation must never be
   call-and-observe.** A host that tried `events/discover` against every
   registered server to find out whether it worked would be probing servers
   that have no reason to expect the call, for a fact the handshake already
   states.

## 10. Conformance checklist (skeleton)

Anchors CI for the events profile and pre-packages the WG submission (§4.2's
"conformance checklist per profile"). Milestone #20 builds the runner.

### Negotiation
- [ ] Declares `io.gleipnir/events` in `server/discover` capabilities.
- [ ] `version` is a valid SemVer string.
- [ ] `heartbeatMs` is present and positive when the server implements `events/listen`.
- [ ] Server does **not** expose event delivery as a tool (§1).

### `events/discover`
- [ ] Returns a `kinds` array; each entry carries a non-empty `kind`.
- [ ] Every `kind` matches a kind attested in the signed manifest (drift is a health fault).
- [ ] `binding_schema`, where present, is valid JSON Schema.

### `events/listen`
- [ ] Response is `Content-Type: text/event-stream`.
- [ ] Every delivered event is a JSON-RPC notification, method `events/event`.
- [ ] Every event's `params` is a valid CloudEvents 1.0 envelope.
- [ ] `gleipnirseq` is present, a `uint64`, lowercase-alphanumeric, ≤20 characters.
- [ ] A clean close sends a JSON-RPC response to the original request id carrying `{reason, cursor}`.
- [ ] An SSE comment heartbeat is emitted at (or better than) the declared `heartbeatMs`.
- [ ] Reconnecting with a prior `cursor` resumes after that `gleipnirseq`, not before it.
- [ ] The server never assumes at-most-once delivery is required of it — redelivery is expected and is the client's problem to dedup.

### Discipline
- [ ] No vendor name appears in normative contract text.
- [ ] No coined vocabulary beyond the two method names, the notification method name, and `gleipnirseq` (§2).

---

## Appendix: worked examples

*Non-normative. This is the only section where concrete event sources appear.*

### A. A code-forge webhook relay

Declares kinds like `issue.opened`, `issue.closed`, `pull_request.merged`,
each with a `binding_schema` narrowing on repository, label, or priority.
High-cardinality binding fields (repository name, label) are natural
`operators` candidates once ADR-052 selectability ships: `in` for "any of
these labels," `eq` for an exact repository match.

*GitHub is one such forge.* It is named here and nowhere else in this
document.

### B. A home-automation hub

Declares kinds like `sensor.threshold_crossed`, `device.state_changed`. A
`heartbeatMs` on the low end (these hubs typically hold a persistent
connection to the devices they proxy, so a stale `events/listen` stream is
worth detecting quickly) and a `binding_schema` keyed on device id and
threshold value.

*Home Assistant is one such hub.* Named here only.

### C. Email — worked in §9.1

No `io.gleipnir/events` declaration at all. Included in this appendix only
to point back at §9.1 rather than to add a new example — the point of §9.1
is that there is nothing further to work out here.

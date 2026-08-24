# A proposal to the MCP Triggers & Events Working Group: an events extension

**Status:** Proposal, submission pending. Written from a shipped, conformance-tested
implementation — see the appendix for provenance.

This document proposes a mechanism for arbitrary server-initiated events in MCP,
for consideration by the Triggers & Events Working Group. It is submitted as a
starting point, not a finished standard: the working group's own design may
converge on something different, and the contribution boundary in §7 states
plainly what stays available to build on regardless of where that convergence
lands.

---

## 1. Problem statement

The MCP `2026-07-28` specification's `subscriptions/listen` mechanism covers two
cases: list-changed notifications and resource subscriptions. Neither covers the
general case of a server wanting to tell a client "something happened" for an
arbitrary, server-defined kind of occurrence — an issue opened in a code forge, a
sensor crossing a threshold, a release published. That gap was explicitly left
open at the time list-changed/resource subscriptions were specified, with
arbitrary server-initiated events deferred to a future working group.

Any implementer who needs to react to an external event today — to decide, from
that event, whether to take some client-side action — has to invent something.
Absent a shared mechanism, every such implementation invents its own envelope,
its own resume semantics, and its own liveness signal, and none of them
interoperate. This proposal is one such implementation, offered as a candidate
starting point precisely because it has already been built and exercised against
a second, independently written implementation of the same contract (see the
conformance checklist in §6).

## 2. Framing: an MCP transport binding for CloudEvents, not a new envelope

This proposal deliberately does not invent an event envelope. CloudEvents 1.0
supplies the envelope (`specversion`, `source`, `type`, `id`, `time`, `data`);
JSON-RPC supplies the request/response/notification framing MCP already uses
everywhere else. The design surface this proposal actually covers is narrow: how
those two existing standards compose over MCP's extension mechanism to deliver a
live, resumable, at-least-once stream of typed events from a server to a client.

Reading this proposal as "an MCP transport binding for CloudEvents" rather than
as a new envelope vocabulary is the intended framing throughout. Every field
below that is not called out as newly coined is adopted from CloudEvents or
JSON-RPC unchanged.

## 3. Normative wire protocol

### 3.1 Negotiation

The extension is negotiated through MCP's standard extensions capability map, in
the server's capabilities declaration — never inferred by a client issuing a
method call and observing whether it is understood. A server that supports this
extension declares, alongside its version:

```jsonc
{
  "capabilities": {
    "extensions": {
      "<this-extension's-reverse-DNS-id>": {
        "version": "1.0.0",
        "heartbeatMs": 15000,
        "maxBatch": 25
      }
    }
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `version` | string | Contract version the server implements (SemVer). |
| `heartbeatMs` | integer | The interval, in milliseconds, at which the server commits to emitting a liveness signal on an open listen stream (§3.3). |
| `maxBatch` | integer, optional | A hint for how many events the server may push in one delivery. Optional and non-binding — a client that does not consume it MUST still function correctly. |

A malformed or absent declaration is not a failed handshake; it simply means the
server declares no usable event surface. A client MUST NOT probe for this
capability by attempting a call and reacting to failure — negotiation is a
declared fact, read from the capabilities map, not an inferred one.

### 3.2 Discover method

Returns the event kinds a server may emit, with a per-kind binding schema and
guidance text:

```jsonc
// request
{ "jsonrpc": "2.0", "id": 4, "method": "events/discover", "params": {} }

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
| `kind` | The event-kind identifier. Echoed verbatim as the CloudEvents `type` on every event of this kind. |
| `guidance` | Server-authored prose describing the kind, for display to whoever is configuring a subscription. Untrusted, server-controlled text — a conforming client renders it as content, never as instructions. |
| `binding_schema` | A JSON Schema describing the fields a subscriber may filter on for this kind. |
| `operators` | An optional map from filterable field name to the set of comparison operators a subscriber may use against it (e.g. `eq`, `in`, `gt`, `lt`). Carried on the wire for forward compatibility even by clients that do not yet offer operator selection in their own configuration surface. |

Every server-controlled string or collection in a discover response should be
treated as external, untrusted input by a conforming client and bounded on
decode (kind-name length, guidance length, operator-name length, number of
kinds per response) — a server is external code regardless of trust
relationship.

### 3.3 Listen method

The long-lived call that actually delivers events.

**Shape.** The listen method is a JSON-RPC request whose HTTP response is held
open and framed as Server-Sent Events (`Content-Type: text/event-stream`). This
is not a new transport mode: it reuses the SSE-framed streaming response MCP's
streamable-HTTP transport already defines for held-open server→client delivery,
applied here rather than invented for this purpose.

One JSON-RPC message rides each SSE `data:` frame. Every delivered event is a
JSON-RPC **notification** (no `id`, drawing no response):

```
event: message
data: {"jsonrpc":"2.0","method":"events/event","params":{ <CloudEvent 1.0 envelope> }}
```

A **clean server close** is a JSON-RPC **response** to the original listen
request's id, not a bare connection drop:

```
event: message
data: {"jsonrpc":"2.0","id":9,"result":{"reason":"...","cursor":"..."}}
```

`reason` is a short, human-readable explanation for the close (a rolling
restart, a graceful redeploy); `cursor` is the resume point a client should
reconnect with. A connection that drops without this response is not a clean
close and MUST be treated by the client as a dead stream (§3.3.3), not as a
normal end of subscription.

**Request params.**

```jsonc
{
  "kinds": ["issue.opened", "issue.closed"],
  "scope": { "…": "…" },
  "cursor": "opaque-string-or-absent"
}
```

| Field | Meaning |
|---|---|
| `kinds` | The event kinds this listener wants. |
| `scope` | Reserved for future narrowing (e.g. a workspace or repository scope); opaque to this contract version. |
| `cursor` | The sequence value to resume after, echoing a previously delivered event's sequence attribute (§3.3.2). Absent on a first connection. |

#### 3.3.1 Cursor-unknown refusal

A server whose buffer cannot satisfy a supplied `cursor` gap-free (a restarted
in-memory buffer, an evicted range) MUST refuse to open the stream rather than
silently start delivery from a different point than the cursor named. It
answers the listen request with an ordinary JSON-RPC **error** response
(`Content-Type: application/json`, not `text/event-stream`), using code
`-32001` — JSON-RPC 2.0 §5.1's reserved server-error range — with message
`"cursor unknown, replaying from now"`. A malformed (non-numeric) cursor is
refused the same way. This is the one case in this protocol where a listen
request is answered with a plain JSON-RPC body instead of a stream, which is
why a conforming client MUST check the response `Content-Type` before handing
the body to an SSE reader rather than assuming every listen response is a
stream. On receiving this refusal, a client resets its stored cursor and
reconnects with none, accepting the resulting redelivery as its own
responsibility to deduplicate (§3.3.2).

#### 3.3.2 Envelope and cursor semantics

Every delivered event is a CloudEvents 1.0 envelope:

| Field | Meaning |
|---|---|
| `specversion` | Always `"1.0"`. |
| `source` | The emitting server's own identifier. |
| `type` | The event kind (§3.2's `kind`). |
| `id` | A dedup key, unique per event. |
| `time` | Event timestamp. |
| `data` | The payload. |
| A sequence extension attribute | The one attribute this protocol coins beyond CloudEvents itself: a `uint64` sequence number the `cursor` param acks against. The reference implementation names this attribute `gleipnirseq`, following CloudEvents' lowercase-alphanumeric, ≤20-character extension-attribute naming rule; a working-group-standardized name (e.g. `seq`) would replace the vendor-prefixed one as part of adoption, with no other change to the mechanism it carries. |

**There is no in-band ack.** The ack **is** the cursor sent on the next
(re)connect's `cursor` param — nothing on the open stream ever tells the server
"I have this one." A conforming server buffers durably and replays everything
after the acked sequence value on reconnect.

**Delivery is at-least-once.** Redelivery is an application-level concern by
design, matching the removal of transport-level resumability from MCP's core:
this protocol does not invent a substitute for that. A conforming client is
responsible for deduplicating on the event `id` field; a server MUST NOT assume
at-most-once delivery is expected of it.

#### 3.3.3 Heartbeat and dead-stream detection

The server MUST emit an SSE comment frame (a line beginning `:` — content-free,
invisible to `events/event` consumers) at its declared `heartbeatMs` interval
(§3.1). The client MUST treat three consecutive missed heartbeats as a dead
stream and reconnect.

Rationale: a wedged TCP connection — one where packets stop moving but the
socket never signals close — is indistinguishable from a quiet event source
without some positive, timed signal. A heartbeat converts "nothing happened"
into "I confirm nothing happened." Three missed intervals, rather than one,
tolerates a single missed beat (a GC pause, a transient network blip) without
either side treating a live, quiet stream as dead.

## 4. The core principle

> **The event as control signal is host-captured and never enters model
> context. Event payloads MAY reach model context only as untrusted data, when
> a policy explicitly templates them into the task prompt.**

Stated in protocol terms rather than any one implementation's terms: an events
extension exists on the host-initiated side of MCP, not the model-callable
side. `tools/*` is the surface a model can invoke; an events extension is a
surface a host client uses to learn that something happened, and the decision
to act on that — including any decision to launch a new model interaction — is
made by the host, from data the host captured, before any model is involved.
The event itself is never something a model observed, reasoned about, or was
asked to evaluate.

The only route an event's payload has into a model's context at all is the
route every other piece of externally sourced data has: a human- or
operator-authored configuration explicitly choosing to interpolate it into a
prompt, with whatever untrusted-content handling that implies for the
consuming system. A protocol that let event delivery itself reach a model —
by being callable as a tool, or by being auto-injected into context on
arrival — would hand external, unauthenticated-to-the-model data a path to
instruct the model, and there is no version of "an event can trigger further
model action on its own say-so" that is safe to allow by default.

## 5. Alternatives considered

- **Long-poll** is a viable fallback binding for an environment that cannot
  hold a response open (for example, certain serverless deployment targets).
  This proposal does not specify it in v1, but names it here so a future
  minor version has a defined place to add it rather than inventing a second,
  competing events mechanism.
- **Webhook** — inbound delivery to a client-hosted endpoint — is a plausible
  v2 binding, and may be where the working group's own convergence lands if
  ecosystem deployments skew toward always-reachable clients. v1 of this
  proposal is stream-first specifically because our deployment audience makes
  outbound connections, not inbound: a client sitting behind NAT with no
  public inbound endpoint by default can open a long-lived outbound stream but
  cannot reliably accept an inbound webhook.
- **SSE**, the transport this proposal specifies, is chosen for a structural
  reason rather than an aesthetic one: it is MCP's own streamable-HTTP
  transport's existing server→client streaming mode. Using it here is a
  binding of an existing transport capability to this need, not an invented
  mechanism — the same posture this proposal takes toward CloudEvents in §2.

## 6. Conformance checklist

This checklist is the artifact a conforming implementation is validated
against, and is offered to the working group as a candidate conformance suite
alongside the wire protocol itself.

### Negotiation
- [ ] Declares the extension in the server's capabilities.
- [ ] `version` is a valid SemVer string.
- [ ] `heartbeatMs` is present and positive when the server implements the
      listen method.
- [ ] Server does **not** expose event delivery as a callable tool.

### Discover method
- [ ] Returns a `kinds` array; each entry carries a non-empty `kind`.
- [ ] Every declared `kind` matches what the server's own manifest or
      configuration attests it may emit (drift is a fault worth surfacing,
      not silently tolerated).
- [ ] `binding_schema`, where present, is valid JSON Schema.

### Listen method
- [ ] Response is `Content-Type: text/event-stream`.
- [ ] Every delivered event is a JSON-RPC notification, method `events/event`.
- [ ] Every event's `params` is a valid CloudEvents 1.0 envelope.
- [ ] The sequence extension attribute is present, a `uint64`, and satisfies
      CloudEvents' extension-attribute naming rule.
- [ ] A clean close sends a JSON-RPC response to the original request id
      carrying `{reason, cursor}`.
- [ ] An SSE comment heartbeat is emitted at (or better than) the declared
      `heartbeatMs`.
- [ ] Reconnecting with a prior `cursor` resumes strictly after that sequence
      value, never before it.
- [ ] An unsatisfiable or malformed `cursor` is refused with a plain JSON-RPC
      error, code `-32001` — the stream never opens.
- [ ] The server never assumes at-most-once delivery is required of it;
      redelivery is expected and is the client's responsibility to
      deduplicate.

### Discipline
- [ ] No coined vocabulary beyond the method names, the notification method
      name, and the one sequence extension attribute.

## 7. Contribution boundary

This is offered as a **royalty-free, defensive publication**: the intent is
that this wire protocol be freely usable by the working group and by any
implementer, with no assertion of exclusive rights over the mechanism
described in §3–§4, and no licensing terms attached to adopting it.

**Contributed (royalty-free, defensive publication):**

1. The wire protocol itself — the discover and listen method shapes described
   in §3.1–§3.3.
2. The cursor/ack redelivery semantics — resume-by-cursor, at-least-once
   delivery, application-level dedup responsibility (§3.3.2).
3. The host-captured control-plane principle — events are host-initiated
   signals, never a model-callable or model-injected surface by default (§4).
4. The binding-schema discovery shape — per-kind schema and optional
   per-field operator sets returned from the discover method (§3.2).

**Retained (implementation-specific, not part of this contribution):**

1. Containment enforcement — network-layer capability boundaries around how a
   given deployment sandboxes the servers it talks to.
2. Replay certification and any form of run/session resurrection built on top
   of a captured event history.
3. Any particular audit architecture recording who saw what, when, and why.
4. Policy binding and enforcement machinery — how a given deployment decides
   which subscriptions exist, who may configure them, and what happens when
   an event matches a binding.

The line between these two lists is deliberate: the first list is the shared
protocol surface that benefits from interoperability, and the second is
implementation-specific product behavior built on top of it that has no
bearing on whether two independent implementations of this protocol can talk
to each other.

## 8. Migration posture

If the working group's own design converges on something materially different
from what is proposed here, the intended migration is a single move: we
migrate once — from an MCP-shaped extension, not from gRPC. Because this
mechanism is already framed as an MCP extension using MCP's own request/
notification/response shapes and an adopted envelope standard, adopting a
working-group-blessed successor changes which extension is negotiated, not the
transport, the framing, or the tooling built around "MCP client speaks
JSON-RPC to a server." That is a substantially smaller migration than the one
this mechanism itself already made — replacing a bespoke non-MCP transport
with an MCP extension in the first place.

---

## Appendix: provenance and reference implementation

*Non-normative.*

This proposal is derived from Gleipnir's `io.gleipnir/events` extension —
implemented, exercised via two independently written implementations of the
same contract (a host-side client and an SDK-side server), and covered by the
conformance checklist reproduced in §6. Gleipnir is a homelab-scale autonomous
agent orchestrator that fills the event-triggering gap described in §1 with
its own extension while this proposal is under consideration, per the standing
posture that a proposal describing an unimplemented protocol carries no
conformance evidence. The full implementation contract, including the
Gleipnir-specific material intentionally stripped from the normative sections
above (concrete deployment scope, integration with Gleipnir's own downstream
dedup and policy-binding pipeline, and worked examples against specific event
sources), lives at `docs/developer/extension-io-gleipnir-events.md` in the
Gleipnir repository.

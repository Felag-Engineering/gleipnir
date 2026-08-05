# `io.gleipnir/channel` — MCP extension contract

**Version:** 1.0.0 · **Status:** implemented (host client) · **Spec:** `mcp-realignment-spec.md` §4, §6.4 · **ADR:** ADR-055 Amendment 1

This document is the normative contract. Per spec §4.2 discipline, no vendor
appears in normative text; concrete channels appear only in the
[examples appendix](#appendix-worked-examples).

---

## 1. Why this is an extension and not a tool

Amendment 1 states the rule this contract exists to satisfy: **host-initiated ⇒
not a tool.**

Delivering a message to a human is something the host decides to do. A model
never asks for it, and a model must never be able to. If channel delivery were
reachable through `tools/call` it would be a grantable capability — something a
policy could hand to an agent, and therefore something an agent could be talked
into using to reach an operator's inbox. Extensions are host-plane surface: a
policy cannot grant one, because there is nothing there to grant.

The same reasoning produced `io.gleipnir/events` (§5). The two extensions are
siblings: one carries signals *into* the host, one carries messages *out*, and
neither is ever addressable by a model.

## 2. Design constraint: coin no vocabulary

The extension defines two methods and almost nothing else. `channel/request`'s
payload is elicitation-shaped — `message`, an optional `requestedSchema`, and
`options` — and the wait it opens is a literal Tasks-extension task, polled with
`tasks/get` and terminated with `tasks/cancel`.

This is a constraint, not a coincidence. **If this extension ever grows its own
request/response vocabulary, that is the signal it has drifted off the standard**
and should be re-derived from it. Durability, restart-resume, poll cadence, and
TTL are all problems the Tasks extension already solved; solving them a second
time here would mean maintaining a second, worse solution.

## 3. Versioning and steward obligations

SemVer from birth, with the same deprecation discipline Gleipnir expects of MCP
itself (§5's steward clause, which §4 inherits). The moment a third party
implements this extension, that obligation is real:

- **Patch** — clarifications; no wire change.
- **Minor** — additive only: new optional fields, new enum members that a
  conforming implementation may ignore.
- **Major** — anything a v1 implementation would misread. Two-major-version
  deprecation window before removal, matching ADR-042's plugin-service policy.

The current version is `1.0.0`, declared by both sides in the capability entry.

## 4. Negotiation

The extension is negotiated through the standard `extensions` capability map. On
the 2026-07-28 transport this appears in the **`server/discover`** result, which
is the modern handshake — the transport is stateless and never runs
`initialize`, so `server/discover` is the only place a modern server declares
anything.

```jsonc
// server/discover result
{
  "supportedVersions": ["2026-07-28"],
  "capabilities": {
    "extensions": {
      "io.gleipnir/channel": {
        "version": "1.0.0",
        "assurance": "authenticated",
        "deliveries": ["direct", "shared"]
      }
    }
  }
}
```

**Both methods require the 2026-07-28 transport.** A legacy-pinned client
refuses to issue them rather than sending a request whose task handle the
transport would drop.

### 4.1 Declaration fields

| Field | Type | Meaning |
|---|---|---|
| `version` | string | Contract version the server implements. |
| `assurance` | `authenticated` \| `weak` | How strongly the channel authenticates the human who acts. See §5. |
| `deliveries` | array of `direct` \| `shared` | Delivery targets the server supports. |

**A server that declares no `deliveries` supports none.** The host does not
assume `shared` as a floor: assuming a broadcast capability that is not there
would route a question to nobody instead of failing where an operator can see it.

**Unknown enum members are dropped, not rejected.** A future member this host
does not know is a capability it cannot reason about, so it is treated as absent.

**A malformed declaration is not a failed handshake.** It decodes to the zero
value, which resolves no elicitation kind and supports no delivery. A broken
channel declaration must never stop a server's *tools* from working, and
fail-closed defaults mean tolerance here cannot widen anything.

Three states are distinguishable, and routing needs all three:

| State | Meaning |
|---|---|
| not declared | The server does no channel work. Routing to it is a configuration error. |
| declared, readable | Use as declared. |
| declared, unreadable | A broken channel plugin. Resolves nothing; worth surfacing as a health fault. |

## 5. Assurance levels (spec §4.1)

Channels differ in how strongly they authenticate an actor. A button click in a
channel that authenticates its users arrives with an identity; an email's `From:`
header is forgeable.

The declared level governs **what a channel is allowed to settle**, and the rule
is the **host's**, evaluated before a request is issued. A rule enforced by the
party it constrains is not a rule.

| Assurance | May settle `information` | May settle `permission` |
|---|---|---|
| `authenticated` | yes | yes |
| `weak` | yes | **no** |
| unrecognized | no | no |

The asymmetry is deliberate. A wrong *value* from a weak channel is a wrong value
the agent then acts on visibly. A forged *approval* is indistinguishable from a
real one after the fact — it is evidence of oversight that never happened.

A weak channel does not *fail* a permission request. The dispatcher falls through
to the next audience entry, so a low-assurance channel degrades routing rather
than breaking it.

**Unrecognized levels resolve nothing.** Guessing upward is the one direction
that cannot be undone.

## 6. Delivery vocabulary

Two members, both channel-neutral:

- **`direct`** — addresses one person privately.
- **`shared`** — addresses a space several people can see.

This vocabulary replaces "DM", which spec §4.2 lists as a scheduled-for-removal
vendor-ism. A target also carries an **`address`**: the channel's own opaque
identifier. The host never parses it — what a channel calls an address is the
channel's business, and a host that understood the format would be a host with a
per-channel special case.

## 7. `channel/notify`

Fire-and-forget delivery.

```jsonc
// params
{
  "target":  { "delivery": "shared", "address": "<opaque>" },
  "message": "<host-authored text>"
}
// result: {}
```

Returns as soon as the channel accepts the message. **Whether a human read it is
unknowable**, and a channel claiming otherwise would be claiming more than it can
know — which is exactly why anything needing an answer uses `channel/request`.

**Ordered fan-out across an audience is the dispatcher's job**, not the plugin's.
One call, one target. A plugin handed a list and told to iterate would be
deciding routing policy, which belongs to the host.

`message` is host-authored, but a plugin must still render it as **content**:
the host may be relaying an untrusted payload inside it.

## 8. `channel/request`

Ask a human something. Returns a Tasks-extension task.

```jsonc
// params
{
  "target":  { "delivery": "direct", "address": "<opaque>" },
  "message": "<the question>",
  "requestedSchema": { "type": "object", "properties": { … } },   // optional
  "options": [ { "id": "approve", "label": "Approve" } ],          // optional
  "kind": "permission"                                             // optional hint
}
// result: a Tasks task envelope
{ "taskId": "…", "status": "working", "pollIntervalMs": 5000, "ttlMs": 3600000 }
```

**A request must carry `options` or `requestedSchema`.** Neither is not a
question — it is a notification that would leave a task open forever, waiting for
an answer the human has no way to give. The client rejects it and points the
caller at `channel/notify`.

**`kind` is a rendering hint, not an enforcement point.** The §5 assurance gate
runs host-side before the request is issued.

**A result without a `taskId` fails the call.** Without a handle the wait is
unaddressable: it cannot be polled, cancelled, or resumed after a restart.

### 8.1 Lifecycle

| Step | Mechanism |
|---|---|
| open | `channel/request` → task in `working` |
| poll | `tasks/get`, honoring the server's `pollIntervalMs` |
| resolve | task reaches `completed`; read `result` |
| terminate | `tasks/cancel` |
| expire | task reaches a terminal non-`completed` state on the server's TTL |

Nothing here is specific to this extension. That is the point.

### 8.2 Resolution payload

```jsonc
{
  "optionId": "approve",              // for a pick-one ask
  "content":  { "ticket": "OPS-1" },  // for a schema ask
  "actorExternalId": "<opaque>"
}
```

A result carrying **neither** `optionId` nor `content` records no decision and is
an error. Reading it as an empty resolution would be reading a non-answer as an
answer.

`actorExternalId` is the channel's **claim** about who acted. How much that claim
is worth is precisely what `assurance` measures. Audit records store both (§6.6)
so an approval reads as evidence rather than as an assertion.

Identifier fields are length-bounded on decode: they reach audit records and
logs, and a plugin is external code.

## 9. Rule-of-three paper validation (spec §4.2)

A profile contract does not freeze until three deliberately dissimilar cases walk
through it without special-casing. If one needs an exception, **the profile is
wrong, not the example.**

### 9.1 Email — high latency, no buttons, weak actor auth

| Contract element | Outcome |
|---|---|
| `assurance` | `weak`. A `From:` header is forgeable. |
| `deliveries` | `direct` (to an address); `shared` (to a list). Both natural. |
| `channel/notify` | Send a message. Exact fit. |
| `channel/request` with `options` | Rendered as reply-with-a-keyword, or per-option mailto/magic links. `optionId` comes back either way. |
| `channel/request` with `requestedSchema` | A reply-parsed form, or a link to a hosted form. |
| Task lifecycle | Hours or days in `working`. Nothing in the contract assumes promptness. |
| `tasks/cancel` | Optional follow-up mail; the task goes terminal regardless. |
| §5 gate | May settle `information`. **May not settle `permission`** — a forged reply approving a production change is exactly the failure the gate exists for. |

**Verdict:** fits with no special cases. The weak-assurance path is not a
degraded mode bolted on for email — it is the reason the level exists.

### 9.2 An event-only source (e.g. a home-automation hub, a code-forge webhook feed)

Such a source emits signals and has no human surface at all.

**Verdict:** the profile is **omitted cleanly**. It declares
`io.gleipnir/events` and simply does not declare `io.gleipnir/channel`. Nothing
in the contract requires a partial or stub implementation, and the "not declared"
state (§4.1) is a first-class answer rather than an error — routing to it is a
configuration mistake the host can name precisely.

This is the case that would have exposed a contract requiring every server to
answer channel methods. It does not.

### 9.3 A vanilla ecosystem MCP server (tool-only)

An off-the-shelf server exposing `tools/list` and `tools/call` and nothing else.

**Verdict:** **omitted cleanly**, and — importantly — *unchanged*. It never sees
a channel method, because negotiation happens in the handshake rather than by
attempting a call and observing the failure. A host that probed by calling would
be delivering messages to find out whether it could.

This is also the case that validates the extension boundary from §1: a tool-only
server's tools are grantable, and its (nonexistent) channel surface is not.

### 9.4 What the exercise changed

Two contract decisions come directly from these three walkthroughs:

1. **`deliveries` has no default.** The event-only and tool-only cases made it
   obvious that "declared nothing" must be readable as "supports nothing", not
   silently widened to a floor.
2. **The §5 gate degrades rather than fails.** Email can legitimately serve an
   audience; refusing its `permission` requests outright would have made
   low-assurance channels unusable instead of merely limited.

## 10. Conformance checklist (skeleton)

Anchors CI for the channel profile and pre-packages the WG submission (§4.2's
"conformance checklist per profile"). Milestone #20 fills in the runner.

### Negotiation
- [ ] Declares `io.gleipnir/channel` in `server/discover` capabilities.
- [ ] `version` is a valid SemVer string.
- [ ] `assurance` is `authenticated` or `weak`.
- [ ] `deliveries` lists only declared-supported targets.
- [ ] Server does **not** expose channel delivery as a tool (§1).

### `channel/notify`
- [ ] Accepts a valid target and returns a result envelope.
- [ ] Rejects an unknown `delivery` value.
- [ ] Rejects an unknown `address` with a JSON-RPC error, not a silent success.
- [ ] Renders `message` as content, never as markup or instructions.

### `channel/request`
- [ ] Returns a task envelope carrying a non-empty `taskId`.
- [ ] Initial `status` is non-terminal.
- [ ] Renders `options` as distinct choices; the returned `optionId` matches one.
- [ ] Renders `requestedSchema` as a form; the returned `content` validates.
- [ ] Sets `actorExternalId` on every completion.
- [ ] Declares `ttlMs` when it enforces a TTL.
- [ ] Honors its own `pollIntervalMs`.

### Task lifecycle
- [ ] `tasks/get` reports the task until terminal.
- [ ] `tasks/cancel` drives a non-terminal task terminal.
- [ ] `tasks/cancel` on a terminal task does not resurrect it.
- [ ] TTL expiry produces a terminal non-`completed` state, not a fabricated answer.
- [ ] A terminal task's result carries `optionId` or `content` — never neither.

### Discipline
- [ ] No vendor name appears in normative contract text.
- [ ] No request/response vocabulary beyond the elicitation shape and Tasks (§2).

---

## Appendix: worked examples

*Non-normative. This is the only section where concrete channels appear.*

### A. A chat platform with authenticated users and interactive buttons

Declares `assurance: "authenticated"` and both delivery targets. A
`channel/request` with `options` renders as a message with buttons; the click
handler completes the task with `{optionId, actorExternalId}` — the platform's
own user id. Because assurance is `authenticated`, this channel may settle
`permission` requests.

Poll-on-signal (§6.4) applies: the click-time `AuthorizeActor` host callback
doubles as a poll hint, so the host polls immediately instead of waiting for the
next interval tick. This closes the latency gap to an in-memory waiter with no
new protocol surface.

*Slack is one such platform.* It is named here and nowhere else in this document.

### B. Email

Walked through in §9.1. `assurance: "weak"`; information only.

### C. A hypothetical divergent channel: a physical annunciator panel

Notify-only hardware — a light and a buzzer. It declares
`deliveries: ["shared"]`, implements `channel/notify`, and does **not**
implement `channel/request`, since it has no input surface.

The contract accommodates this without a new concept: a server declaring the
extension but supporting no request path simply errors on `channel/request`, and
the audience falls through. Worth noting as the boundary case where "declared the
extension" and "can answer questions" come apart — a future minor version could
add an explicit `methods` list to the declaration if this shape becomes common
enough to be worth negotiating rather than discovering.

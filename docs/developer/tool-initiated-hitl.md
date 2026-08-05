# Writing a tool that asks a human

For authors of MCP servers (and, after the cutover, Gleipnir plugins) whose
tools need a person to decide something mid-call.

**Spec:** `mcp-realignment-spec.md` §6 (ADR-055, Amendment 1) ·
**Operator-facing version of this page:** [Human-in-the-loop](../user/human-in-the-loop.md)

## The shape

Your tool is called. Instead of returning a result, you return an MRTR
`input_required` carrying one or more elicitations and an opaque `requestState`
blob of your own. Gleipnir pauses the run, routes the question to a human, and
**re-issues the identical `tools/call`** with `inputResponses` and your
`requestState` attached.

You are not being resumed. You are being called again.

```
tools/call  ──►  input_required { inputRequests[], requestState }
                      │
                      │   (host pauses the run, asks a human)
                      ▼
tools/call  ──►  result            ← same name, same arguments,
   + inputResponses[]                plus the answer and your blob back
   + requestState
```

## The obligation that comes with it

**MRTR is stateless by design, so "start over" is always available to the
host — which means any side effect you performed before asking is a side effect
that can happen twice.** Idempotency across the pre-elicitation portion of your
call is yours, not the host's.

This is not theoretical. Gleipnir re-issues the original call in at least two
ordinary situations: after a human answers, and again if your `requestState`
expired while they were thinking (§6.5). A tool that charges a card, opens a
ticket, or sends a message *before* asking for confirmation will do it once per
attempt.

Put the side effect after the answer, or make it idempotent under a key you
carry in `requestState`.

## Saying which kind of ask it is

Two kinds, and they are answerable by different roles:

- **permission** — consent only. Approve or reject. Needs an `approver`.
- **information** — values you need. A form. Needs an `operator`.

Gleipnir infers it from the shape: a `requestedSchema` that asks for no fields
is a permission ask; anything with fields is an information ask. When one result
bundles several requests, *information wins* — a single field anywhere means the
person must be shown a form rather than an approve/reject pair.

Say it explicitly when you can, with `_meta`:

```json
{
  "inputRequests": [
    {
      "message": "Delete 12 production records?",
      "requestedSchema": { "type": "object", "properties": {} },
      "_meta": { "io.gleipnir/elicitation-kind": "permission" }
    }
  ],
  "requestState": { "cursor": "abc-1" }
}
```

A `_meta` kind outside the vocabulary is **ignored**, not rejected — it is an
optional hint from a server that got it wrong, and the schema shape is still
readable. Do not rely on a misspelling being caught.

## Rules your elicitation must follow

**Never ask for a secret.** A `requestedSchema` containing `format: "password"`,
`writeOnly: true`, or `x-gleipnir-secret` causes Gleipnir to **refuse the entire
request** — not to redact the field. Form mode does not carry secrets, full stop.
A redacted field would leave the human answering a question they cannot see and
you waiting on a value you will never get.

**Your `message` is untrusted text.** Gleipnir renders it as characters
everywhere — never as markup, never as instructions. Do not put formatting in it
expecting it to be interpreted, and do not expect a human to follow directions
embedded in it.

**A URL in the message becomes an explicit "open in browser" step**, with the
hostname shown separately and nothing auto-opened. Only `http` and `https` are
offered as links at all. If your flow continues in a browser, put a plain URL in
the message and expect the human to have to click deliberately.

**Ask something answerable.** An elicitation with neither options nor a schema
is a notification wearing a question's clothes, and would leave a task open
forever waiting for an answer nobody can give.

## What comes back

`inputResponses`, correlated to your `inputRequests` **by position** — MRTR
carries no per-request id, so the count matches exactly. Each entry is one of:

| action | carries content | means |
|---|---|---|
| `accept` | yes | the human agreed, and supplied values if asked for |
| `decline` | no | the human refused |
| `cancel` | no | the exchange was abandoned |

A `decline` is a **legitimate answer**, not an error. Gleipnir hands it back to
you and you decide what it means — an error result, a partial result, or a
different question. Do not treat it as a transport failure.

## Caps you will hit if you misbehave

Hard limits, not heuristics. Repetition fatigue-trains approvers, which is the
worst possible conditioning for the people the mechanism depends on, so the caps
bound requests absolutely rather than merely spacing them out.

| Cap | Default | Effect |
|---|---|---|
| Per-run elicitation budget | policy's `max_elicitations_per_run` (unset = unlimited) | Fail-closed. Over budget ⇒ the call fails structurally, the run continues. |
| Per-server rate limit | 1/s sustained, burst 5 (`GLEIPNIR_ELICITATION_RATE_PER_SEC` / `_BURST`) | Over-limit results are refused **before decoding**. Token bucket. |
| Requests per result | 8 (`GLEIPNIR_ELICITATION_MAX_REQUESTS`) | Structural error; nothing persisted. |
| `inputRequests` bytes | 64 KiB (`GLEIPNIR_ELICITATION_MAX_REQUESTS_BYTES`) | Structural error. |
| `requestState` bytes | 16 KiB (`GLEIPNIR_ELICITATION_MAX_REQUEST_STATE_BYTES`) | Structural error. |
| Rounds per single call | 8, not tunable | The call is abandoned. Answering every retry with another question is not a conversation. |

Keep `requestState` small. It is persisted for the length of a human's
attention span, and 16 KiB is generous for a cursor.

## Deadlines

Three clocks can govern one wait, and the precedence is fixed (§6.3):

1. **Gleipnir's policy timeout is authoritative for the human leg.** When it
   expires the host stops waiting — it will fire `tasks/cancel` where a task is
   involved, or simply abandon the retry.
2. **Your TTLs are weather.** Real, honored, surfaced to the operator as their
   own distinct failure — but never the authority over how long Gleipnir waits
   for a person.
3. The operator sees the **minimum** of whichever apply, labelled with which one
   produced it.

## When your state expires mid-wait

The recovery path, and the reason to keep your re-asks stable:

- Your `requestState` expires while a human is thinking.
- Their answer is spent on a retry you no longer recognize, so you start over
  and ask again.
- **If you ask the identical question** — same message, same schema — Gleipnir
  replays the stored answer against your fresh `requestState` automatically. The
  human never sees the hiccup.
- **If you ask anything different**, the human is re-prompted with the previous
  question and answer attached, flagged as a change.

Equality is over the message text and the canonicalized schema. A cosmetically
reworded re-ask is a *different* question and will interrupt someone, so do not
regenerate prompt text non-deterministically.

The replay happens **once per question**. Answering a replay with the same
question a third time is a loop, not a recovery, and falls through to the human.

Generous TTLs and durable tasks make this path rare. Prefer them.

## Delivering the question yourself (channel servers)

If your server *is* a channel — Slack, email, an annunciator panel — the
mechanism is different and is documented separately: the `io.gleipnir/channel`
extension, in [extension-io-gleipnir-channel.md](extension-io-gleipnir-channel.md).

Two things carry over from this page:

- Declare your **assurance** honestly. `authenticated` means the channel
  identifies its actors; `weak` means actor identity is forgeable or absent. A
  `weak` channel is never routed a permission request — the audience falls
  through to the next entry. Over-declaring does not get you more traffic, it
  gets forged approvals into an audit trail.
- The `kind` is sent to you so you can render a permission prompt differently
  from a form. It is **not** the enforcement point. The assurance gate runs
  host-side before your server is contacted, because a rule enforced by the
  party it constrains is not a rule.

## Related

- [Human-in-the-loop](../user/human-in-the-loop.md) — the operator's view.
- [extension-io-gleipnir-channel.md](extension-io-gleipnir-channel.md) — the
  channel contract, task lifecycle, and conformance checklist.
- [mcp-realignment-spec.md](mcp-realignment-spec.md) §6 — the design record.

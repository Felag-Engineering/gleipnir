# Human-in-the-loop

Three different things in Gleipnir can stop a run and wait for a person. They
look similar in the UI and they are not the same, so it is worth knowing which
one you are looking at before you click.

| | Who decides to ask | When | Can the agent avoid it? | What your answer does |
|---|---|---|---|---|
| **Agent-initiated** | The agent | Whenever it chooses | Yes — it simply doesn't ask | Goes back to the agent as text it reasons about |
| **Policy-gated approval** | You, in the policy | Before the tool runs | **No** | Lets the call through, or blocks it outright |
| **Tool-initiated** | The tool server | Mid-call, after it started | No, but the *server* chose to ask | Goes back to the **server**, which decides what to do with it |

The first two are described in [Policies](policies.md). This page is mostly
about the third, which is new.

## Tool-initiated requests

A tool call goes out. Instead of a result, the server answers *"I need a human
to tell me something first."* Gleipnir pauses the run, puts the question in
front of you, and — once you answer — re-issues the same call with your answer
attached.

Two things are worth understanding about that:

**The agent never sees the exchange.** Its view of the call is the ordinary
call → result pair, exactly as if the server had answered the first time.
Everything about the pause lives in the audit record, not in the agent's
context. That is deliberate: an operator's answer is oversight evidence, not
input the model gets to reason about or be steered by.

**It is cooperative, not a gate.** A policy-gated approval is Gleipnir refusing
to let a call happen. A tool-initiated request is the *server* asking, and your
answer — including a refusal — is handed back to it. The server decides what a
refusal means: an error, a partial result, or a different question. Both can
happen on one call, and the trace shows that order.

### Permission or information

Every request is one of two kinds, and the difference decides who may answer it.

**Permission** — a consent-only ask. "Delete 12 production records?" You approve
or reject; there is nothing to fill in. Resolvable by **approvers** (and admins),
the same authority as a policy-gated approval, because that is what it is: an
authorization decision.

**Information** — a request for values. "Which region should the rollout
target?" You get a form built from what the server asked for. Resolvable by
**operators** (and admins), because supplying an operating parameter is
operating work, not an authorization.

Gleipnir decides which is which from the shape of the request: an ask with no
fields to fill in is a permission ask; anything with fields is an information
ask. A well-behaved server can also say explicitly which it means.

Anyone who can see a run — including **auditors** — can see the question. Only
the required role gets the buttons. An auditor who cannot see what was asked
cannot audit the decision that followed.

### The text comes from the server

The question, the field labels, and any link in the message are written by the
tool server, not by Gleipnir. The UI renders them as plain text, never as
formatting and never as instructions, and says so on the card.

If the message contains a link, Gleipnir shows it as an explicit **"Open in
browser"** step with the destination **hostname displayed on its own line**.
Nothing opens automatically and nothing is embedded in the page. Read the
hostname before you click it — a link that reads like one domain and goes to
another is the exact thing that display exists to expose.

Forms never ask for secrets. If a server requests a password-shaped field,
Gleipnir refuses the whole request rather than rendering it — a form that
silently dropped the field would leave you answering a question you could not
see.

### Where you'll see them

- **Attention queue** (dashboard and runs page) — a `TOOL ASK` row, ordered by
  deadline alongside approvals and feedback requests. There is no one-click
  approve here on purpose: the question, its form, its deadline, and any earlier
  attempt only exist on the run page, and approving without having read them is
  not consent. The button opens the run.
- **Run detail** — the card, with the buttons if you hold the role.

## Deadlines: three clocks, one answer

Up to three different timers can be counting down on the same request:

1. **Gleipnir's own timeout**, from the policy (or the system default). This is
   the authoritative one for the human leg — when it runs out, Gleipnir stops
   waiting and the run fails with a feedback timeout.
2. **The server's task expiry.** Some servers put a lifetime on the work they
   are holding open for you.
3. **The server's saved-state expiry.** Some servers say how long the opaque
   blob they handed us stays valid.

The countdown on the card is the **minimum of whichever apply** — the *effective
deadline*, because that is the moment something actually changes. The card also
names which clock produced it, since running out means different things:

| Deadline source | What happens when it lapses |
|---|---|
| `policy` | Gleipnir gives up waiting. The run fails with a feedback timeout. |
| `server_ttl` | The server abandons the work before Gleipnir would have. Surfaced as its own distinct failure, not conflated with nobody answering. |
| `request_state` | The server's saved state expires. Usually invisible — see below. |

Server-side timers are treated as **weather**: real, worth showing you, but not
the authority over how long Gleipnir is willing to wait for a person.

### When a server's state expires while you're thinking

This is the case most likely to look strange, so it is worth describing.

You are looking at a question. Meanwhile the server discards the state it was
holding. Your answer arrives, gets spent on a retry the server no longer
recognizes, and the server starts over by asking again.

**If it asks the identical question**, Gleipnir replays the answer you already
gave, against the server's fresh state, without showing you anything. You will
never know it happened. Making you re-answer a question you just answered gains
nothing and trains people to click through prompts.

That replay happens **once per question**. A server that answers the replay with
the same question a third time is not recovering from an expiry, it is looping —
so the second identical re-ask falls through to you, where you can see something
is wrong.

**If it asks a different question**, you get a new prompt with the previous
question and your previous answer shown above it, flagged as *"You already
answered a different question."* A second prompt that looks like a duplicate but
is not is exactly where a reflexive approval does the most damage.

## Where the question gets delivered

The in-app card is one route. A policy can also point requests at an
**audience** — an ordered list of channels — and the first channel that is
allowed to settle *this kind of request* gets it.

"Allowed to" is the host's decision, from what the channel declares about how
strongly it knows the person acting on it:

| Channel assurance | Can supply **information** | Can grant **permission** |
|---|---|---|
| `authenticated` — the channel identifies its actors | yes | yes |
| `weak` — actor identity is forgeable or absent (email being the classic case) | yes | **no** |
| unrecognized or not declared | no | no |

The asymmetry is deliberate. A wrong answer on a weak channel is an answer the
agent then acts on visibly. A **forged approval** on a weak channel is
indistinguishable from a real one after the fact, and an approval record that
cannot tell them apart is not evidence of anything.

A channel that may not settle a request is **skipped, not failed** — the request
moves to the next entry in the audience. The built-in in-app channel is appended
last unless you turn it off, and it is `authenticated` (the person signed in to
*this* Gleipnir), so a permission request refused by every configured channel
still reaches somebody.

If you *have* turned the in-app fallback off and every channel is skipped, the
request fails loudly rather than sitting unanswered. That is a configuration
problem worth hearing about immediately.

Only one channel is asked. An audience is a priority list, not a broadcast —
asking two channels the same question would put two people in a race whose loser
is told their decision did not count.

## Limits a misbehaving server will hit

A server can ask for human attention. It cannot ask for unlimited human
attention. Three caps apply, and they are hard rather than heuristic — repetition
is how approvers get fatigue-trained, which is the worst possible conditioning
for the people the whole mechanism depends on.

| Cap | Default | What happens at the limit |
|---|---|---|
| **Per-run elicitation budget** | unlimited unless the policy sets `max_elicitations_per_run` | The call fails structurally; the run continues. Nobody was interrupted, so nothing is left waiting. |
| **Per-server rate limit** on requests | `GLEIPNIR_ELICITATION_RATE_PER_SEC` = 1/s, `GLEIPNIR_ELICITATION_BURST` = 5 | Over-limit responses are refused before they are even decoded. A token bucket, not a debounce — a debounce spaces an attack out without capping it. |
| **Size caps** | `GLEIPNIR_ELICITATION_MAX_REQUESTS` = 8 questions, `GLEIPNIR_ELICITATION_MAX_REQUESTS_BYTES` = 64 KiB, `GLEIPNIR_ELICITATION_MAX_REQUEST_STATE_BYTES` = 16 KiB | The call fails and nothing is persisted. |

Two more structural backstops that are not tunable: a single call may pause for
input at most **8 times** before Gleipnir abandons it, and an elicitation asking
for a secret is refused outright.

An exhausted budget or an abandoned call fails **the call**, not the run — the
agent sees an error result and can route around the misbehaving tool. An
*unanswered* request is different: a person was interrupted and nobody answered,
so the run fails.

Setting `max_elicitations_per_run` in a policy is the lever worth reaching for
first if a tool is being noisy. It is fail-closed: once spent, there is no grace
and no way for a server to earn more by waiting.

## What gets recorded

Every settled request writes one **decision record** — separate from the run's
reasoning trace, and never shown to the agent. It captures:

- which channel was asked, and that channel's assurance level;
- who acted (the channel's identifier for them) **and** whether Gleipnir could
  tie that identifier to a Gleipnir user, and how;
- whether the ask was permission or information;
- the effective deadline and which clock set it;
- the outcome: answered, rejected, timed out, cancelled, or auto-replayed;
- which audience entries were skipped on the way, and why.

"An approval happened" is not oversight evidence. The record is this wide so
that two materially different events cannot produce the same row — an approval
by a verified person on a strong channel and one by an unverifiable name on a
weak one must not read alike.

A **permission** granted by an actor Gleipnir could not link to a user is flagged
at `warning` severity rather than `info`. That is the shape a forged approval
takes, and it should not require opening every record to find.

Read them at `GET /api/v1/runs/{run_id}/decisions`, readable by every role that
can see the run, auditors included.

## Related

- [Policies](policies.md) — trigger types, capability grants, approval gating.
- [Roles](roles.md) — who can do what.
- [MCP protocol migration](mcp-protocol-migration.md) — what changes on a
  2026-07-28 server.
- [Operations](operations.md) — the environment variables named above.
- For MCP server authors: [Writing a tool that asks a human](../developer/tool-initiated-hitl.md).

# Tool-provider profile — the baseline

**Version:** 1.0.0 · **Status:** contract defined; checks run by `plugin-sdk/conform` (#977) · **Spec:** `mcp-realignment-spec.md` §4 · **ADR:** ADR-053 · **Checklist:** [`plugin-sdk/conform/checklists/tool-provider.md`](../../plugin-sdk/conform/checklists/tool-provider.md)

This document is the normative contract for the baseline profile. Per spec
§4.2 discipline, no vendor appears in normative text; concrete servers appear
only in the [examples appendix](#appendix-worked-examples).

---

## 1. What the profile is

A managed plugin that declares `tool_provider` is an MCP server whose tools an
agent can be granted. The profile adds **no protocol obligations beyond MCP
itself**: no extension to declare, no method to implement, no Gleipnir library
to link. `tools/list` and `tools/call` are the whole surface, and the server
learns nothing about Gleipnir from serving them.

Everything a managed plugin gets beyond that (containment, lifecycle,
signature-pinned identity, audit, approval gating, parameter scoping) is done
*to* the server by the host, not *by* it.

## 2. Obligations

**None beyond MCP.** A server qualifies if it speaks the 2026-07-28 protocol
over the streamable HTTP transport (§3) and serves well-formed `tools/list` and
`tools/call`.

## 3. Install constraints the host derives

These are not protocol obligations. They follow from how the host uses a tool
once it has one, and a server that breaks them is not wrong as an MCP server.
It is a server the host cannot safely put into its shared tool namespace.
Each is a checklist item, and the checklist names which package enforces it
on the host, or says that none does yet (§5).

1. **Protocol: 2026-07-28, modern transport.** A managed endpoint is
   registered pinned to `2026-07-28`. The host sends stateless `tools/list`
   and `tools/call`: no `initialize`, no session. A server that only speaks an
   earlier protocol revision is out of scope for the alpha (§6.3).
2. **Namespace charset.** Every tool is granted as `<source>.<tool>`, where
   `<source>` is the instance name. The tool part must be 1–128 characters
   from `[A-Za-z0-9_.-]`. That is the current MCP specification's
   recommendation, made mandatory here, because the name travels through
   policy YAML, a JSON-RPC routing header and every LLM provider's tool-name
   rules. Excluding `/` is also what keeps items 4 and 5 structural.
3. **Schemas canonicalize.** Each `inputSchema` must be accepted by
   `internal/schemanorm` (valid UTF-8, no duplicate object keys, at most 1 MiB,
   depth 64, 10,000 nodes) and must compile as JSON Schema. The canonical form
   is what drift detection compares and what argument validation runs
   against, so a schema that does not canonicalize leaves the tool with
   neither. Byte-level canonicalization stays at install time; the checklist
   asks only that it succeed.
4. **No host-plane names.** No tool may carry the `host/` prefix. The host
   endpoint's own methods are `host/*` tools on a *different* server (spec §8),
   and anything in the shared namespace is grantable to an agent.
5. **No extension-method names.** No tool may be named after an
   `io.gleipnir/*` method (`channel/notify`, `channel/request`,
   `events/discover`, `events/listen`, `events/event`). Spec §4's invariant
   is *if the host initiates it, it is not a tool*; a tool by one of these
   names is that invariant broken from the other side.
6. **Manifest agreement.** Every tool named in the manifest's `gleipnir.tools`
   (which carries per-tool metadata such as `elicitation_kind`, and is not a
   tool inventory) must appear in `tools/list`. Metadata for a tool the server
   does not serve is consent to something that does not exist.
7. **Profile agreement.** A server that serves tools declares `tool_provider`.
   The host refuses every grant on an instance whose manifest does not declare
   it, so tools served without the declaration are unusable rather than
   unsafe. This is the one constraint with no checklist item: the checklist
   runs only for plugins that declare the profile.
8. **Unknown calls fail.** `tools/call` for a name the server does not serve
   returns an error (a JSON-RPC error, or an `isError` result), never a
   success.

## 4. Optional: a plugin with no tools

The profile is optional like every other. A plugin that declares only
`event_source`, or only `human_channel`, serves an empty `tools/list` and
none of §3 applies to it. Validated in §6.2.

## 5. Where the host enforces this today

The checklist is the authority on each item's status; this is the summary.

| §3 item | Host enforcement |
|---|---|
| 1 Protocol | Pin asserted at registration (`internal/mcp`, `managed.go`). **Not enforced against the server:** see §6.3. |
| 2 Charset | **None.** Only cross-source uniqueness (`internal/toolregistry`). |
| 3 Schemas | `internal/schemanorm` via `internal/mcp` at discovery, **fail-open**: the tool is kept with a NULL canonical schema and no argument validation. |
| 4 `host/` | `internal/plugin/hostendpoint.AssertHostPlane`, **once at boot**, against the exact eleven-name inventory. It runs before managed tools are discovered, so it cannot see them. |
| 5 Extension names | **None.** |
| 6 Manifest ⊆ `tools/list` | **None.** Grants are checked against *discovered* tools at run start (`internal/plugin/assembly`, managed tool classifier). |
| 7 Profile agreement | `internal/plugin/assembly` managed tool classifier refuses every grant at run start. |
| 8 Unknown calls | Not checked. The host never calls a tool it has not discovered, and maps any failure to an `is_error` tool result (`internal/mcp`, `internal/execution/agent`). |

Until host-side enforcement exists for the unenforced items, the conformance
runner (`plugin-sdk/conform`, #977) is the only check, and it runs at author
time, not install time.

## 6. Rule-of-three paper validation (spec §4.2)

### 6.1 Email — a source with weak actor authentication

An email plugin's natural surface is a human channel (`io.gleipnir/channel`,
walked through in that contract's §9.1). It may *also* offer tools: search a
mailbox, draft a reply. Those are ordinary tools, model-plane, grantable, and
approval-gateable like any other.

**Verdict: fits without special cases.** The one place email tests this
profile is the line between "send a message" as a **tool** (the agent decides
to send; grantable; approval can gate it) and as **channel delivery** (the
host decides to ask a human; never grantable). Both can exist in one plugin,
and §3.5 keeps them from colliding.

### 6.2 Event-only — a plugin with zero tools

A plugin that declares only `event_source` and serves an empty `tools/list`.

**Verdict: installs and runs healthy, verified against the code.**

- `manifestv2.Validate` requires at least one declared profile, of any kind;
  an empty `gleipnir.tools` is valid.
- The loader has no tool requirement.
- The attach pass registers the managed endpoint; an empty `tools/list` is a
  successful first discovery with no drift.
- `caphealth` liveness is the container healthcheck plus `server/discover`;
  it never consults tools. It seeds `event_source` healthy and faults it only
  on events-extension drift.
- The managed tool classifier's refusal applies to grants, and an event-only
  plugin has none.

End to end, the DooD suite's events test (`internal/plugin/dood`) runs a
zero-tool stub through install, approval, reconcile and a container restart to
completed runs.

### 6.3 A vanilla ecosystem MCP server — tool-only

An off-the-shelf server serving `tools/list` and `tools/call` and nothing
else, wrapped in an image with a manifest and a signature.

**Verdict: fits only if it speaks 2026-07-28. Earlier revisions are an open
gap.** The contract needs no special case; the substrate does. What happens
today with a server that only speaks an earlier revision (`initialize`
handshake, sessions, no `server/discover`):

- **Registration** pins the endpoint to `2026-07-28` without probing.
- **Discovery** sends stateless modern-shaped `tools/list`. A stateful legacy
  server rejects it; `RefreshTools` errors and the attach pass logs a WARN
  and keeps the row with zero tools. It never retries an unchanged URL, so
  nothing recovers until an operator refresh or a rotation. Every grant then
  fails at run start with "has not discovered a tool named …".
- **The pin never demotes.** A probe that classifies the server as legacy is
  logged at WARN and the modern pin is kept. A managed row's URL is not
  operator-editable, so there is no operator path to change it.
- **Liveness still passes.** The `server/discover` probe falls back to a
  legacy `initialize`, and the liveness check accepts any successful probe
  without looking at the protocol era. The instance reads as live while its
  tools are unusable.
- A legacy server that tolerates session-less requests may work, by accident
  of that server's implementation rather than anything the host guarantees.

For the alpha, then, the claim is **any MCP server speaking the 2026-07-28
protocol** qualifies with zero code changes. A bilingual managed client, or
refusing a legacy-only server at install, is unresolved.

### 6.4 What the exercise changed

1. **Email** is why §3.5 is an install constraint rather than advice: the
   same plugin can legitimately hold a send-message tool and a
   channel-delivery method, and only the names keep them apart.
2. **Event-only** confirmed that "no tools" needs no stub and no special
   path, matching the events contract's lesson that an omitted profile is
   silent.
3. **The vanilla server** narrowed the spec §4 claim and exposed the
   liveness-versus-usability split recorded in §6.3.

---

## Appendix: worked examples

*Non-normative. This is the only section where concrete servers appear.*

### A. A code-forge MCP server

A server such as GitHub's official MCP server exposes issue, pull-request and
repository tools under names like `create_issue` and `list_pull_requests`. All
fit §3.2 unchanged, and its tool-only shape is §6.3 exactly: it qualifies if
the image speaks 2026-07-28, and hits the §6.3 gap if it does not.

### B. A home-automation hub

A Home Assistant plugin is §6.2's case if it only streams state changes as
events, and gains `tool_provider` the moment it also offers a "turn this light
on" tool. The two profiles are independent, so adding the tools changes
nothing about its event source.

### C. Email — worked in §6.1

No vendor is needed: the email case is defined by the protocol's properties
(high latency, forgeable sender), not by any product.

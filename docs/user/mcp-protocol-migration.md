# MCP protocol migration

This page is for operators running MCP servers on a pre-2026 protocol revision. Gleipnir is bilingual: a server that speaks `2024-11-05`, `2025-03-26`, or `2025-06-18` keeps working exactly as it does today, with byte-identical request shaping. Nothing on this page requires operator action unless one of your servers upgrades to the `2026-07-28` revision.

## How Gleipnir decides which protocol a server speaks

On **Add MCP server** and on **Rediscover**, Gleipnir sends one `server/discover` POST before it discovers the server's tools. The probe classifies the server into one of three outcomes:

- **Modern** — the server answers with a recognizable `2026-07-28` response and shares `2026-07-28` in common with Gleipnir. The server is pinned to `2026-07-28`.
- **Legacy** — the server does not answer as a `2026-07-28` server. Gleipnir falls back to the legacy `initialize` handshake and pins whichever of `2024-11-05`, `2025-03-26`, or `2025-06-18` the server reports — or `2024-11-05` if it reports nothing recognizable.
- **Inconclusive** — the server answers with a status that carries no protocol signal (401, 403, 429, or a 5xx). Nothing is pinned: the existing pin, if any, is kept, and the next **Rediscover** probes again.

Gleipnir speaks exactly one modern revision today: `2026-07-28`. A confirmed-modern server that shares no version in common with Gleipnir is treated as an error, not a downgrade — the existing pin is left alone.

The pin is stored per registry entry and returned by `GET /api/v1/mcp/servers` as `protocol_version` (`null` means the server has never been probed).

**What does NOT re-probe.** This is the most likely operator surprise: editing a server's name or URL (`PUT /api/v1/mcp/servers/{id}`) does not re-probe the protocol, and there is no periodic background rediscovery. After changing a server's URL, press **Rediscover**.

**No automatic downgrade.** Once a server is pinned to `2026-07-28`, a later probe that classifies it as legacy does not demote it — Gleipnir logs a warning and keeps the pin. This is deliberate: one bad 4xx from a WAF or proxy in front of the server must not silently strip away the newer transport's protections. To reset a modern pin, delete the server entry and re-register it — there is no "reset pin" button or endpoint. This has two side effects worth knowing before you do it: deleting the server deletes every tool row registered under it, so re-registering re-discovers every tool freshly enabled, silently undoing any tool you had deliberately disabled; and the server's configured auth headers are deleted with it, so restoring them afterward needs the original secret value — Gleipnir never lets you read a configured header back out.

The protocol probe and tool discovery share a single deadline of 2 × `GLEIPNIR_MCP_TIMEOUT`. **Rediscover** can fail outright if a slow-but-reachable server misses that deadline. **Add MCP server** cannot: both probes are fail-open there, so a timeout instead creates the server unpinned (`Protocol unknown`) with no tools and a `discovery_error` recorded — see the log table below. Don't read "the server was created" as "the probe succeeded."

## Reading the badge on the Tools page

Every server card on **Tools** (`/tools`) shows a protocol badge:

| Badge | Meaning |
|---|---|
| `Protocol 2026-07-28` | Pinned to the current protocol revision. |
| `Legacy protocol` | Pinned to a pre-2026 revision. |
| `Protocol unknown` | Never probed — press **Rediscover**. |

The card's tooltip refers to the entry as a "source". The badge is visible to admin, operator, and auditor roles. **Rediscover** is authorized for admin and operator only — the button is not hidden from an auditor, but the request is rejected.

## What changes when a server is pinned to 2026-07-28

| Behavior | Applies to |
|---|---|
| Stateless requests: no `initialize`, no `notifications/initialized`, no `Mcp-Session-Id`, and no automatic re-initialize retry on HTTP 401 — a 401 is now an auth failure, full stop. | `2026-07-28` pin only |
| `MCP-Protocol-Version`, `Mcp-Method`, and (on tool calls) `Mcp-Name` headers set by Gleipnir on every POST. | `2026-07-28` pin only |
| `_meta` carrying `protocolVersion`, `clientInfo`, and `clientCapabilities` on every request. Gleipnir declares no capability on any call path today, and there is no way to make it declare `sampling`. | `2026-07-28` pin only |
| `x-mcp-header` tool-parameter headers honored. | `2026-07-28` pin only |
| `ttlMs`/`cacheScope` discovery cache hints honored. | `2026-07-28` pin only |
| `resultType` on tool results: an absent value is read as `"complete"`. | All servers, unconditionally — this rule exists *for* older servers. |
| The array of tools sent to the LLM is sorted by name on every run. | All runs, unrelated to the pin. |

For a legacy-pinned or never-probed server, request bytes are unchanged from before this work.

## Sessions and the "12-month deprecation window"

The `2026-07-28` revision removes sessions: a modern-pinned server gets no `Mcp-Session-Id` header and no handshake.

The "12-month deprecation window" is a plan-level statement recorded in [the MCP realignment spec](../developer/mcp-realignment-spec.md) §11 (and mirrored in the reserved-header list's comment). **No start date, end date, or cutoff is encoded anywhere in Gleipnir**, and no code path expires or refuses a legacy pin.

What you can rely on today: `Mcp-Session-Id` stays on the reserved header-name list, alongside `Mcp-Method`, `Mcp-Name`, `Mcp-Protocol-Version`, `Content-Type`, `Accept`, `Content-Length`, and `Host`. Configuring an auth header with one of those names is rejected outright; a header of that name that was stored before the name was reserved is dropped at request-build time with a warning (see [What to look for in the logs](#what-to-look-for-in-the-logs) below).

There is no removal date for legacy support, and this page will not invent one.

## The "Simplified for …" notice

The chip renders on a tool row inside a server's detail panel on **Tools**. The label reads `Simplified for Google` for one or two providers; three or more collapse to `Simplified for N providers` (the tooltip always names every provider).

What it means: Gleipnir shows that provider's wire a rewritten version of the tool's parameter schema. This runs entirely downstream of enforcement — it changes only what the model is shown and never widens what the tool is allowed to receive.

Which providers appear is computed per request from the providers currently configured. Today only Google's wire is restricted; Anthropic, OpenAI, and OpenAI-compatible backends declare full schema support and never appear.

**Correction to a common assumption:** the chip is computed from the tool's stored canonical schema. A tool that has no stored canonical schema gets no chip at all — not a fallback guess. Three things cause that: the server has not been rediscovered since the canonical-schema column was added, the server's schema could not be normalized, or the stored canonical schema is larger than 64 KiB, which skips translation entirely regardless of whether normalization succeeded. Press **Rediscover** to backfill the first two; the third has no remedy on this page. Also: when a schema cannot be presented to a provider at all, that provider is simply omitted from the chip rather than listed — there is no "unsupported" chip.

## `x-mcp-header` tool-parameter headers

A server may annotate a tool input property with `x-mcp-header` so the argument also rides along as an HTTP header on the tool call. This is honored only on `2026-07-28`-pinned servers — a legacy or never-probed server's annotations are never even read.

The annotated property is still sent in the JSON-RPC arguments too; it is not stripped, so the audit trail and validation see the full argument set.

Validation is strict, because the header name is chosen by the remote server and the value by the model. A declared header is rejected if:

- its name is on the reserved header-name list
- its name contains any byte outside `A-Za-z0-9-`
- it is a hop-by-hop or proxy-control header, `Authorization`/`Cookie`, a forwarding or client-IP header, a method-override spelling, or `User-Agent`
- it collides with an auth header configured for that server
- another property on the same call declares the same header name
- the call would need more than 32 headers
- its value is larger than 4 KiB, or contains a CR, LF, NUL, DEL, or any other control character (a plain tab or space is permitted)

**Failure mode operators will actually see:** name validation runs for every annotated property whether or not the model supplied a value, so a bad declaration fails every call to that tool, deterministically. The run itself does not fail — the agent gets an `error` step tagged `schema_violation` plus a `tool_result` marked as an error, and another turn to try again. The `gleipnir_mcp_errors_total{error_type="protocol"}` metric counter increments.

There is no per-tool configuration surface for this. Treat an auth-header collision as a warning sign, not an obstacle: it means the remote server's own schema is asking to control the name of a header you already use to authenticate to it, and the collision check fails the call closed specifically to surface that. The safe response is to disable the tool (`PUT /api/v1/mcp/servers/{id}/tools/{toolID}/enabled`, admin or operator) or remove the tool's grant from the policy — disabling alone is fail-closed, so runs of any policy still granting that tool will fail at start until you also drop the grant. Renaming your configured auth header removes the collision, but it does not remove the risk — it hands the model that header name on that server, and Gleipnir will then send both headers on any call where the model populates that argument: the model's value under the name you just vacated, and your credential under the new name.

Poll triggers send no `x-mcp-header` headers, by design.

## Discovery caching on 2026-07-28 servers

A modern-pinned server may attach a `ttlMs`/`cacheScope` hint to its tool list response. Gleipnir honors it, clamped to a hard 60-second ceiling no matter what the server asks for. `cacheScope: "private"` is not cached at all — it fails closed. A malformed or absent hint means "always fetch", exactly as it does today.

The practical consequence: a second **Rediscover** within the cache window may not reach the server at all. The discover API response carries `served_from_cache`, but the UI does not display it today. The drift flag is only recomputed on a real fetch, so a cache hit never clears a drift warning. Tool-call results are never cached — the `2026-07-28` schema carries no cache hint on a tool call result — and the only thing reused across poll ticks is the resolved client, which saves the per-tick legacy handshake.

## What to look for in the logs

See [Operations — Viewing structured logs](operations.md#viewing-structured-logs) for how to stream and filter JSON logs. These are the exact `msg` strings to grep for:

| Message | Level | When |
|---|---|---|
| `mcp protocol version pin changed` | WARN | Any pin change; fields include `previous_pin`, `new_pin`, `era`. |
| `mcp protocol probe failed; keeping existing pin` | WARN | A Rediscover probe failed; the existing pin is unchanged. |
| `MCP protocol probe failed on server create` | WARN | The probe on **Add MCP server** failed; the server is still created. |
| `mcp protocol probe would downgrade an established modern pin; keeping existing pin, explicit operator action required` | WARN | A probe classified a `2026-07-28`-pinned server as legacy. The pin is not downgraded — see "No automatic downgrade" above. |
| `mcp server identified itself` | INFO | The server's self-reported name and version, seen on a modern probe. |
| `mcp tools/list cache hit: server requested caching via ttlMs/cacheScope, skipping the tools/list network round trip` (`mcp tools/list cache hit:` is a sufficient grep prefix) | INFO | A Rediscover was served from the discovery cache instead of reaching the server. |
| `dropping stored mcp auth header: name is reserved and cannot be sent` | WARN | A grandfathered auth header collides with a newly reserved name and is dropped before the request is sent. |

```bash
docker compose logs api | jq 'select(.msg == "mcp protocol version pin changed")'
```

## What is decided but not shipped

The protocol probing, pinning, badge, `x-mcp-header`, and discovery-caching behavior described on this page has shipped, and so has **tool-initiated human-in-the-loop** — a 2026-07-28 server can now answer a tool call with `input_required` and Gleipnir will pause the run and ask a person. That is documented in [Human-in-the-loop](human-in-the-loop.md); MCP server authors want [Writing a tool that asks a human](../developer/tool-initiated-hitl.md).

The rest of the MCP 2026 realignment — plugins repackaged as signed containerized MCP servers and the `io.gleipnir/events` extension — is **decided but not implemented**. The v1.1 plugin system described in [Plugins](plugins.md) remains the live plugin architecture. See [the realignment spec](../developer/mcp-realignment-spec.md) for the full design.

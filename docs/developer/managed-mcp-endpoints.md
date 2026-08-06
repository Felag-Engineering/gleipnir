# Managed MCP endpoints

**Spec:** `docs/developer/mcp-realignment-spec.md` §3 (ADR-053). **Issue:** #819.
**Code:** `internal/mcp/managed.go`, `internal/mcp/gate.go`.

A healthy managed-plugin generation is an MCP server entry like any external
one, plus a trust tier.

## One client stack, not two

The realignment's claim is that a plugin stops being something Gleipnir talks
to over a bespoke gRPC dispatcher and becomes a server it talks to over the same
transport, through the same discovery, into the same `<source>.<tool>`
namespace, with the same canonical-schema persistence.

That claim is only true if a managed plugin's endpoint is an ordinary
`mcp_servers` row. Anything else — a parallel table, a synthetic in-memory
entry, a "plugin server" variant — reintroduces the second path the realignment
exists to delete, and every consumer downstream would need to know which kind it
was holding.

So it is one row, and one nullable column tells the two apart.

## The tier is derived, not stored

`mcp_servers.plugin_instance_id` is the whole distinction. `TrustTierOf` reads
it; there is no `trust_tier` column.

A separate column would be a second fact that must agree with the first, and two
facts that must agree are two facts that can disagree. A row could then claim to
be managed while pointing at no instance, or claim to be external while the
reconciler rotates its URL underneath it. Deriving costs one branch and cannot
drift.

`ON DELETE CASCADE`, unlike `plugin_audit_events.plugin_instance_id`, which is
`SET NULL`. An audit event is a record **of** an instance and must outlive it;
this row is a route **to** one, and a route to a deleted instance is a dangling
endpoint the agent could still resolve a tool through.

## Rotation: one row per instance, not per generation

`Register` is idempotent and repoints in place. A rotation updates the `url`; it
does not create a second entry.

That matters twice over:

- **The namespace prefix survives.** The server name is the `<source>` half of
  every tool's dot-name. A prefix that changed across a rotation would
  invalidate every policy's tool grants at the exact moment of an upgrade.
- **The routing flip needs no explicit invalidation.** `url` is part of
  `serverConfig` (`cache.go`), the registry cache's invalidation key. The next
  resolve rebuilds the client against the new address on its own, while a
  `*Client` already handed to a running run keeps the base URL it was built with
  and **drains against the generation it started on**. Retiring the old
  container is the rotation's own drain step, not something this layer does.

`Deregister` deletes the row, which cascades to `mcp_tools`. That is what
releases the instance's tool names: reservations are rebuilt from the DB, so the
namespace frees itself rather than needing a second bookkeeping call a future
path could forget.

Both operations are idempotent because the caller is level-triggered. "Already
registered" and "already gone" are the common outcomes of a pass.

### Protocol pinning

A managed entry is pinned to `2026-07-28` at registration rather than probed for
it. That is an assertion, but a **signed** one: the bundle's manifest declares
the protocol and the Minisign signature covers the manifest, so a managed plugin
that does not speak it is a bundle that lied about itself. The `server/discover`
that follows still fails loudly if the container disagrees — pinning decides the
request shape, it does not skip the handshake.

## `io.gleipnir/*` is reserved to managed endpoints

`WithTrustTier` gates extension negotiation. A managed client honours an
`io.gleipnir/channel` declaration; an external one **drops it with a warning**,
and the era classification is untouched either way (dropping an optional
extension must never demote a definitively modern server to the legacy
transport).

These extensions are host-plane. A server that declares `io.gleipnir/channel`
and is believed can be asked to settle a human approval — and nothing about an
external URL an operator pasted in makes it a channel Gleipnir should route
consent through. §5 leaves external extension opt-in explicitly deferred, and
"deferred" has to mean the path does not exist yet, not that it exists
unguarded.

The zero value is external. A client built without an opinion negotiates
nothing private, which is the fail-closed direction.

## Per-server concurrency and queue depth

`WithServerCallLimits` sets a semaphore and a bounded waiting room per server,
claimed in `CallTool` before any header resolution, request build, or socket
work. Defaults are `50 / 50`, matching `internal/plugin/dispatch.Config`
exactly — a plugin moving onto this transport should meet the same ceilings it
met before, because changing them in the same step as changing the transport
would make a regression impossible to attribute.

The two bounds answer different questions and both are needed:

| Bound | Question | Whose problem |
|---|---|---|
| `MaxConcurrent` | How much work is this server doing at once? | The server's |
| `MaxQueueDepth` | How many callers are *waiting*? | Gleipnir's |

Without the second, a wedged server converts every run that touches it into a
blocked goroutine holding a run slot, and the failure spreads from one server to
the whole host. Past the depth the answer is `ErrQueueFull`, returned
**immediately** — the point is not to add one more blocked goroutine.

Three outcomes, deliberately distinguishable:

- `ErrQueueFull` — saturated; this caller never waited.
- A context error — this caller's own deadline expired while waiting.
- Success — a slot was held and is released on return.

An already-cancelled caller is refused before the semaphore select rather than
being left to it, because a `select` with both cases ready picks at random and
the outcome would otherwise depend on a coin flip. The queue check comes first,
so "you were over the ceiling" wins over "you gave up": the former is a fact
about saturation the caller could not have known, decided without any waiting.

Limits are registry-wide with per-server-name overrides, **not** a DB column.
What they bound is Gleipnir's own exposure to a slow server, which is a host
capacity decision — putting it in the servers table would let whoever registered
a server also decide how much of the host it may occupy.

## Not operator-editable

| Operation | External | Managed |
|---|---|---|
| Rename / repoint (`PUT /mcp/servers/:id`) | ✓ | **409** |
| Delete (`DELETE /mcp/servers/:id`) | ✓ | **409** |
| Set / delete auth header | ✓ | **409** |
| Rediscover | ✓ | ✓ *(it is a read)* |

**409, not 403.** The request is not forbidden to this role — an admin has every
permission and still cannot do it, because the conflict is with what the row
*is*.

Renaming one would change the `<source>` half of every tool dot-name and
silently invalidate the tool grants in every policy that uses the plugin.
Repointing the URL would hold until the next rotation overwrote the edit, which
is worse than being refused because it looks like it worked. Deleting one leaves
a running, consented container nothing routes to — the way to remove it is to
remove the instance, which cascades.

A managed endpoint's credentials come from the plugin's own credential surface
(ADR-049) and were consented with the bundle. An operator-set header on top
would travel to the plugin on every call without appearing anywhere the consent
screen shows.

The header guard sits in `withMutatedHeaders`, the single choke point both
header endpoints funnel through — one of two copies is the one a later endpoint
forgets to add.

The API reports `trust_tier`, `plugin_instance_id`, and `editable`; the UI
labels managed rows and offers neither button, because an affordance that always
fails reads as a bug rather than as a boundary.

## Not in scope here

- The host endpoint (M17).
- External-server extension opt-in — explicitly deferred, §5.
- Calling `Register` / `Deregister` from the generation switch. The reconciler
  is not wired into `main.go` yet; this lands the seam the switch will call.

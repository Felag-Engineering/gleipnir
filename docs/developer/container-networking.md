# Container networking for managed plugins

**Status:** part of the MCP realignment (ADR-056, `mcp-realignment-spec.md` §7). The
reconciler that creates these networks is not yet wired into `main.go` — see the
`internal/plugin/reconciler` entry in `internal/plugin/CLAUDE.md`.

## One internal network per instance

Every managed plugin instance gets its own internal-only network. Gleipnir attaches
to all of them; each plugin attaches only to its own.

This is east-west isolation, and it exists because the alternative violates ADR-001
by topology rather than by policy. On a shared network, a compromised plugin could
call a sibling plugin's MCP endpoint directly, and anything else on that network
could invoke plugin tools with no audit trail and no capability check — bypassing
the enforcement Gleipnir's whole design rests on. No amount of policy configuration
fixes that; only the topology does.

`Internal: true` also establishes default-deny egress: a plugin container has no
route off its own network until something deliberately grants one. Manifest-declared
egress grants build on this — the default-deny is established here.

## Subnet allocation

Networks are allocated an explicit `/24` from a configurable base pool
(`GLEIPNIR_PLUGIN_SUBNET_POOL`, default `10.83.0.0/16` = 256 instances).

Gleipnir allocates explicitly rather than letting the container runtime choose,
because stock Docker default address pools exhaust at roughly **30 networks** — a
limit a homelab with a dozen plugins can reach, and one whose failure mode is a
daemon-level error about address space that says nothing about which pool, how many
instances, or what to do next. Gleipnir's own exhaustion error names all three.

Allocation is dense (the lowest free slot is reused, so removing an instance frees
its number for the next one) and race-safe by construction: the allocator writes
through `UNIQUE(pool_base, slot)`, so two concurrent allocators cannot both commit
the same subnet — the loser retries the next free slot.

### Sizing the pool

| Pool | Instances |
|------|-----------|
| `/24` | 1 |
| `/20` | 16 |
| `/16` | 256 (default) |
| `/12` | 4096 |

The pool must be IPv4 and no longer than a `/24`. IPv6 plugin networks are not
supported.

### Daemon `default-address-pools`

Gleipnir's pool governs only the networks Gleipnir creates. If the same daemon also
runs your own workloads, widen the daemon's pool too so the two allocators are not
competing for the same space — and keep the two ranges disjoint:

```json
// /etc/docker/daemon.json (Podman: /etc/containers/containers.conf, [network])
{
  "default-address-pools": [
    { "base": "172.17.0.0/12", "size": 24 }
  ]
}
```

Choose a `GLEIPNIR_PLUGIN_SUBNET_POOL` outside that base. The default
`10.83.0.0/16` is deliberately off the beaten path for exactly this reason.

## Naming and labels

Networks are named from the desired-state row's `network_name` so
`docker network ls` reads as an inventory rather than a puzzle, and carry two
labels:

| Label | Value |
|-------|-------|
| `gleipnir.managed` | `true` — the discovery key; anything without it is invisible to the reconciler |
| `gleipnir.plugin.instance` | the plugin instance ID |

The `gleipnir.managed` label is what keeps an operator's own networks safe: the
reconciler only ever lists, and therefore only ever removes, what carries it.

## Lifecycle

The network is created **before** the container (a container cannot attach to a
network that does not exist) and removed **after** it (removing a network still in
use fails at the socket). The subnet is released only once the network is gone —
releasing earlier could hand a still-in-use subnet to another instance, turning a
clean teardown into a stuck one.

Each of those is a separate reconciler pass. That is the level-triggered contract,
not an inefficiency: every pass re-reads the world, so there is no sequence to
resume after a crash.

## The unbuilt fallback: shared network plus inbound auth

For a deployment with genuinely hundreds of instances, the alternative is a single
shared plugin network with authenticated inbound calls to each plugin's MCP
endpoint — trading topological isolation for authentication-based isolation.

This is **documented, not built**. It is written down so the option is not
rediscovered from scratch, and because the trade-off should be made deliberately:
it moves plugin isolation from a property of the network to a property of a
credential check, which is a materially weaker guarantee. Widening the pool handles
every deployment size we expect to see.

# Container networking for managed plugins

**Status:** part of the MCP realignment (ADR-056, `mcp-realignment-spec.md` §7). The
reconciler that creates these networks is not yet wired into `main.go` — see the
`internal/plugin/reconciler` entry in `internal/plugin/CLAUDE.md`.

## One internal network per instance

Every managed plugin instance gets its own internal-only network. Gleipnir attaches
to all of them; each plugin attaches only to its own.

## Gleipnir joins each network itself — from inside a container

Under rootless Podman, a bridge network's gateway and every container's IP live
inside the rootless network namespace. A Gleipnir process running directly on the
host can neither bind that gateway nor route to a container on it — there is no
vantage point from which a host process can reach the network at all. So Gleipnir
must run **in a container** that itself joins each instance network, the same way
the plugin containers do.

The reconciler does this with two ordinary container-runtime calls,
`ConnectNetwork`/`DisconnectNetwork` on `internal/plugin/container.Runtime`, wrapped
in a `SelfAttacher` (`internal/plugin/container/self.go`) that is constructed once
with the container ID Gleipnir resolved for itself.

### Resolving Gleipnir's own container ID

`container.ResolveSelfContainerID` deliberately does NOT trust this process's own
hostname as a lookup key — a hostname is something an operator (or, in a compromise,
an attacker) can set to name a completely different container, and looking one up by
it would let self-attach be steered onto the wrong container. Instead it reads two
sources the container runtime itself asserted at container-creation time, before this
process's own contents ever ran:

- **Podman:** the `id=` line in `/run/.containerenv`, a file Podman writes into every
  container it starts.
- **Docker:** the `/proc/self/mountinfo` entry for this container's own bind-mounted
  `hostname`/`hosts`/`resolv.conf` files, whose host-side source path names the
  container by ID.

Whichever candidate ID that finds is then corroborated, not trusted blind: `Inspect`
must succeed against it, the inspected container's own ID must equal the candidate
verbatim, and its configured hostname must equal what this process calls itself.
Any failure at any step — no candidate found, an unreachable daemon, a corroboration
mismatch — resolves to empty rather than guessing, and `reconciler.Config.SelfContainerID`
treats empty as "not containerized, skip self-attach entirely" rather than a startup
failure.

### Self-attach's own constraints

`SelfAttacher` refuses to touch a network unless ALL of the following hold, checked
before either `ConnectNetwork`/`DisconnectNetwork` ever reaches the socket:

- the network carries the `gleipnir.managed` label;
- the network is `Internal: true`;
- the network's instance label names the exact instance the caller is acting on;
- the network's subnet falls inside `GLEIPNIR_PLUGIN_SUBNET_POOL`, and (when the
  reconciler's own database already recorded one) matches that specific instance's
  allocated subnet exactly.

That refuses a network Gleipnir did not create, and refuses acting on the wrong
instance's network even if it happens to carry the right generic labels — but see
"What this does and does not prevent" below for what these checks do NOT cover.

Self-attach is additionally gated at construction on Gleipnir's own kernel: it never
activates unless `/proc/sys/net/ipv4/ip_forward` and
`/proc/sys/net/ipv6/conf/all/forwarding` both read `0` inside Gleipnir's own
container. `docker-compose.plugins.yml` sets both via `sysctls:` for exactly this
reason — see below.

Self-attach's write to a network always PINS a specific address
(`egress.GleipnirAddrOf` — the second usable address in the subnet, e.g. `.2`, never
`.1`: the network's own gateway belongs to the bridge itself and can never be
assigned to any container). Pinning is what lets plugins and the host endpoint be
told a single, deterministic address to reach Gleipnir at, regardless of attach
order — see `egress-containment.md` for how the proxy and `ProxyEnv` consume it.

Attach and detach are planned independently of the plugin container's own lifecycle:
a Gleipnir process that was just recreated (a fresh container ID, so it starts
unattached again) rejoins a network whose plugin is ALREADY running and fully
converged, on the very next pass; and an instance the operator stopped is detached
from immediately, whether or not its own container has been removed yet.

Self-attach is its own reconciler step on each side of a container's lifecycle:
`ActionAttachSelf` runs after the network is created and before the plugin container
is (Gleipnir must already be reachable the moment the container starts), and
`ActionDetachSelf` runs after the plugin container is gone and before the network is
removed (a network Gleipnir is still attached to is a network still in use). When
`reconciler.Config.SelfContainerID` is empty — Gleipnir is not running in a container
at all — the loop still converges containers and networks, it just never plans a
self-attach step. A failed `Inspect` of Gleipnir's own container degrades to
"self-attach disabled for this pass" (logged) rather than aborting the whole pass —
the core loop still has containers to converge and orphans to clean up regardless.

**Host-process mode with plugins is unsupported in the alpha.** If Gleipnir is not
running in a container, it cannot reach any plugin's network, and therefore cannot
route MCP calls to it. Running Gleipnir as a bare host process is a normal
development configuration only until plugins enter the picture.

### What this does and does not prevent

Gleipnir's own container ending up with an interface on every instance network is
NOT, by itself, a bridge between those networks. Linux does not forward packets
between two interfaces of the same container unless something turns that forwarding
on; nothing this substrate does enables NAT, iptables `FORWARD` rules, or IP
forwarding inside Gleipnir's own container, and self-attach's own precondition check
above actively refuses to run if forwarding is enabled. What this DOES NOT cover:

- **A compromise of Gleipnir's own container itself.** Self-attach's label/subnet
  checks defend against a *bug* in the reconciler acting on the wrong network — they
  are not a sandbox around Gleipnir. A process that has gained arbitrary code
  execution inside Gleipnir's own container has every address Gleipnir is attached
  to as a starting point, the same way any multi-homed host does.
- **The operator-facing admin API surface.** Whether (and how) an operator-reachable
  API endpoint could be used to pivot across instance networks is tracked as its own
  follow-up (#1021) rather than folded into this document.
- **Verifying which daemon is actually on the other end of the socket.** Posture
  detection (`auto`, `docker`, `rootless-podman`) is a startup-time PROBE of where a
  socket file exists, not a verified attestation of which daemon answers on it —
  see `docker-compose.plugins.yml`'s own comment on this.

### Granting Gleipnir the socket

The stock `docker-compose.yml` mounts no container-runtime socket — Gleipnir cannot
create, start, or network plugin containers by default. `docker-compose.plugins.yml`
is an opt-in override that grants it:

```
docker compose -f docker-compose.yml -f docker-compose.plugins.yml up
```

The socket it mounts is root-equivalent on the host (or equivalent to whichever user
owns it, for a rootless Podman socket) — see the comment at the top of that file
before using it. It also sets the two forwarding sysctls self-attach's precondition
check requires; do not remove them.

This is east-west isolation, and it exists because the alternative violates ADR-001
by topology rather than by policy. On a shared network, a compromised plugin could
call a sibling plugin's MCP endpoint directly, and anything else on that network
could invoke plugin tools with no audit trail and no capability check — bypassing
the enforcement Gleipnir's whole design rests on. No amount of policy configuration
fixes that; only the topology does.

`Internal: true` also establishes default-deny egress: a plugin container has no
route off its own network until something deliberately grants one. Manifest-declared
egress grants build on this — the default-deny is established here.

### Plugin containers drop every capability and gain none back

Every managed plugin container is created with `CapDrop: [ALL]` plus
`SecurityOpt: [no-new-privileges]` — both **required** by `container.ValidateCreate`
(#1021 review item V2), not merely conventional. This tightens an earlier version of
this constraint, which dropped only `NET_RAW` (the capability that lets a container
craft raw and ICMP-adjacent packets — the specific tool an east-west attack mounted
from inside a compromised plugin's own container would reach for) on the reasoning
that an unknown plugin image's entrypoint might still need `CHOWN` or `SETUID` to fix
a mounted volume's ownership or drop privileges itself before running as non-root.
The current bar is stricter: dropping ALL, with an image expected to do any such
ownership fix at build time rather than at container-create time.
`no-new-privileges` closes the door dropping every capability leaves ajar: with no
capabilities left to abuse directly, a setuid-root binary shipped inside the image is
the remaining way a process could regain privilege, and `no-new-privileges` is what
refuses that regardless of what the image ships.

### Instance networks have IPv6 disabled

`CreateNetwork` sets `EnableIPv6` to an **explicit** `false` on every instance
network — required by `container.ValidateCreateNetwork` (#1021 review item V1), and
set explicitly regardless of what the request already said, so a daemon-level default
(some daemons enable IPv6 for new networks unless told otherwise) cannot hand an
instance network ULA addressing Gleipnir never asked for. The self-attach and egress
mechanisms described above are IPv4-only throughout.

A link-local `fe80::` address is a separate exposure from ULA addressing: every Linux
interface autoconfigures one unless IPv6 is disabled outright on it, independent of
whatever address the network's own IPAM hands out — `EnableIPv6: false` on the
*network* does not, by itself, stop an interface from getting one. Defense in depth
here is two layers, not one:

1. **IPv6 disabled in Gleipnir's own network namespace.** `docker-compose.plugins.yml`
   sets `net.ipv6.conf.{all,default}.disable_ipv6: 1` alongside the forwarding
   sysctls, and self-attach's own precondition check
   (`container.CheckForwardingDisabled`, #1021 review item 2) refuses to activate
   unless `net.ipv6.conf.default.disable_ipv6` reads `1` (and, symmetrically,
   `net.ipv4.conf.default.forwarding` / `net.ipv6.conf.default.forwarding` both read
   `0`) — fail-closed on a read error, except that the whole IPv6 branch of the check
   is satisfied outright when `/proc/sys/net/ipv6` does not exist at all (a kernel
   built without IPv6 support has nothing for these switches to guard).
2. **The netguard listener wrapper** (PR #1029, not yet merged) refusing `fe80::/10`
   on both `LocalAddr` and `RemoteAddr` for the operator API listener — the second
   layer, independent of what Gleipnir's own namespace sysctls say, so a
   misconfigured or reverted namespace setting does not silently remove all
   protection. `reconciler.Config.OperatorAPIGuarded` is the structural gate for this:
   self-attach refuses to activate at all until the caller wiring the reconciler
   confirms that guard is actually in front of the operator listener (see the
   `Config` field's own doc comment) — the same fail-closed shape as the forwarding
   check, and deliberately NOT importing the unmerged netguard package to express it.

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

The full ordering, network create through network remove:

1. **create the network** (a container cannot attach to one that does not exist)
2. **Gleipnir self-attaches** (it must be reachable the moment the container starts)
3. create the plugin container, then start it
4. — teardown, in reverse —
5. stop the plugin container, then remove it
6. **Gleipnir self-detaches** (a network Gleipnir is still attached to is still in use)
7. **remove the network** (removing a network still in use fails at the socket)

The subnet is released only once the network is gone — releasing earlier could hand a
still-in-use subnet to another instance, turning a clean teardown into a stuck one.

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

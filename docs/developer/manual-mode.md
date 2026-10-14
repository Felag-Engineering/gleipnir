# Manual mode

**Spec:** `mcp-realignment-spec.md` §7 socket posture 3 (ADR-056) · **Issue:** #817

Manual mode is a supported first-class configuration, not a degraded one. The
operator declares plugin containers in their own compose file; Gleipnir
discovers them by label, health-checks them, and wires them into the MCP client
— and **never writes to the container socket**.

It exists because handing a socket to a long-running service is a real decision
with real consequences, and an operator who declines should still get plugins,
not a broken product with a warning banner.

## What Gleipnir does and does not do

| | Managed posture | Manual posture |
|---|---|---|
| Create / start / stop / remove containers | Gleipnir | **operator** |
| Create / remove networks | Gleipnir | **operator** |
| Discover by label | Gleipnir | Gleipnir |
| Health probe | Gleipnir | Gleipnir |
| Verify the signed bundle | Gleipnir | Gleipnir |
| Wire into the MCP client | Gleipnir | Gleipnir |
| Egress proxy listener for the instance | Gleipnir, at its reserved `.2` on the instance network | **nobody** — see [Egress and the host endpoint](#egress-and-the-host-endpoint) |
| Host endpoint (`host/*` callbacks) | Gleipnir, at the same `.2` | **not served** |

The "never writes" half is enforced by `container.ReadOnlyRuntime`, which fails
every write with `ErrManualModeWrite` — not by the reconciler remembering to
behave. A posture guaranteed by caller discipline is one refactor away from
being untrue.

**Signature verification is unchanged.** Manual mode consumes the same
Minisign-signed bundle for the manifest and the trust decision. Only the
image and container lifecycle is operator-owned; what a plugin is *allowed to
do* is still decided from a signed manifest an admin consented to.

## The label contract

Two labels bind a container to an installed plugin instance:

| Label | Value | Meaning |
|---|---|---|
| `gleipnir.managed` | `true` | The discovery key. Anything without it is invisible to Gleipnir — which is also what keeps an operator's other containers safe. |
| `gleipnir.instance` | the plugin instance ID | Which installed instance this container serves. |

One optional label matters once you start rotating images:

| Label | Value | Meaning |
|---|---|---|
| `gleipnir.generation` | the generation number | Which generation this container runs. Only checked when the instance's desired row tracks one. |

## What discovery concludes

Every one of these is silent by nature — a container absent, or present under
the wrong label — so each gets a named state rather than an instance that is
quietly never routed to.

| State | What happened | What to do |
|---|---|---|
| `matched` | Exactly one container carries the instance's label, at the expected generation. | Nothing. |
| `declared_but_not_found` | The instance is installed; no container carries its label. | Declare it in your compose file, or fix the label — a typo here looks identical to not having started it. |
| `found_but_not_installed` | A container is labelled for an instance Gleipnir does not have. | Usually a stale container from a removed plugin. Gleipnir will not touch it — in manual mode nothing is Gleipnir's to remove — so naming it is all it can do. |
| `wrong_generation` | The container's generation label disagrees with the desired row. | Re-create the container from the approved image. **The plugin running is not the plugin that was approved.** |
| `ambiguous` | Several containers carry one instance's label. | Remove the extras. Gleipnir will not guess which is authoritative: routing to the wrong one is indistinguishable from routing to the right one until something goes wrong. |

## Egress and the host endpoint

Read this before writing a compose file: manual mode gives a plugin **neither**
of the two services Gleipnir normally provides on an instance's network.

In the managed postures Gleipnir pins its own container to a reserved address on
each instance network (`egress.GleipnirAddrOf`: the second usable address, `.2`;
never the gateway `.1`, which is the bridge itself and under rootful Docker is
the host) and binds the egress proxy and the host endpoint there. The attach pass
drives those listeners only when it has pinned that address, and it never does so
in manual posture: your compose file attaches Gleipnir to the network, so its
address there is whatever IPAM assigned, and no per-generation tokens exist
either, because manual mode has no generations.

So in manual posture:

- **No egress proxy.** Nothing listens on `:8118` for Gleipnir, and no
  allowlist is registered for the instance. On an `internal: true` network that
  means the plugin has **no egress at all** — the default deny is absolute. A
  plugin that needs to reach the internet needs a forward proxy *you* run on that
  network (and then the allowlist is yours to enforce and audit; Gleipnir does
  neither). Pointing `HTTPS_PROXY` at Gleipnir's address, at `.1` or at `.2`, does
  not give you its egress proxy.
- **No host endpoint.** The `host/*` tools (`get_instance_config`,
  `get_credentials`, `emit_metric`, `set_health_state`, `authorize_actor`, ...)
  are not served, and no `GLEIPNIR_HOST_ENDPOINT_URL` or
  `GLEIPNIR_INSTANCE_TOKEN` is injected. A plugin that depends on a callback
  cannot run in manual mode. What still works is everything Gleipnir does by
  reading: discovery, the health probe, log capture and stats. Configuration and
  credentials must reach the container through your compose file.

Managed postures are the way to get a contained plugin that can reach the
network and call back to Gleipnir; manual mode trades both for never giving
Gleipnir the socket.

## Reference compose

One internal network per instance, which is the default deny. This file gives the
plugin no egress, per the section above; an optional proxy of your own is shown
commented out.

```yaml
networks:
  # One network per instance. Gleipnir attaches to each so it can reach the
  # plugin's MCP endpoint; the plugin can reach nothing else. Gleipnir's address
  # on it is whatever IPAM assigns: manual mode does not pin one.
  plugin-acme:
    internal: true          # <- the default deny. Without this line there is none.
    ipam:
      config:
        - subnet: 172.30.7.0/24

services:
  gleipnir:
    image: ghcr.io/felag-engineering/gleipnir:latest
    networks:
      - default
      - plugin-acme         # attach to every plugin network
    environment:
      GLEIPNIR_CONTAINER_RUNTIME_MODE: "manual"

  plugin-acme:
    image: ghcr.io/example/acme@sha256:...   # digest-pinned, matching the signed manifest
    networks: [plugin-acme]
    labels:
      gleipnir.managed: "true"
      gleipnir.instance: "<instance-id>"
    # Only if the plugin must reach the internet: a forward proxy you run and
    # attach to this network yourself (Gleipnir does not provide one in manual
    # mode), and point the plugin at it. Gleipnir's address is not the proxy.
    #environment:
    #  HTTPS_PROXY: "http://egress-proxy:3128"
    #  HTTP_PROXY:  "http://egress-proxy:3128"
    #  NO_PROXY:    ""     # <- empty on purpose: nothing bypasses the proxy.
    mem_limit: 256m
    cpus: 0.5
    healthcheck:
      test: ["CMD", "/plugin", "healthcheck"]
      interval: 10s
      timeout: 3s
      retries: 3
```

## The four ways to get this silently wrong

Each of these leaves a stack that looks correct and enforces nothing.

**Omitting `internal: true`.** The container gets ordinary egress and the proxy
becomes decorative — every request still succeeds, just not through the
allowlist. This is the one worth double-checking, because nothing fails.

**Setting `NO_PROXY`.** If you did configure a proxy of your own, each entry is a
hole; `NO_PROXY=*` disables egress containment entirely while every other line
still reads correctly.

**Sharing one network across plugins.** East-west isolation is the reason for
one network per instance: on a shared network a compromised plugin can call a
sibling's MCP endpoint directly, with no audit and no capability check — an
ADR-001 violation by topology rather than by code.

**Omitting `mem_limit` / `cpus`.** Gleipnir cannot apply cgroup caps it is not
allowed to set. An uncapped container on a homelab host is one plugin away from
an OOM that takes Gleipnir with it.

## Health, logs, and stats in manual mode

All three keep working, because all three are reads:

- **Health** — container healthcheck plus `server/discover`, the same two-part
  liveness the managed posture uses.
- **Logs** — stdout/stderr capture over the socket's log API, same labels, same
  caps.
- **Stats** — the same stats API, so the memory numbers on the admin page are
  the real cgroup figures rather than an estimate.

What is unavailable is anything requiring a write: no automatic restart, no
generation rotation, no image GC, no network lifecycle — and, as above, no
egress proxy or host endpoint. Automating a lifecycle
action in manual mode would be a contradiction in terms — the whole point is
that the operator owns it.

## Related

- [Container networking](container-networking.md) — the per-instance network model.
- [Egress containment](egress-containment.md) — what the proxy enforces and how.
- [Plugins](../user/plugins.md) — installing, signing, approving.

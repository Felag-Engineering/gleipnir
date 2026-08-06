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

## Reference compose

Implements the same posture the reconciler would: one internal network per
instance, egress only through Gleipnir's proxy.

```yaml
networks:
  # One network per instance. Gleipnir attaches to each so it can reach the
  # plugin's MCP endpoint; the plugin can reach nothing else.
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
      GLEIPNIR_CONTAINER_MODE: "manual"

  plugin-acme:
    image: ghcr.io/example/acme@sha256:...   # digest-pinned, matching the signed manifest
    networks: [plugin-acme]
    labels:
      gleipnir.managed: "true"
      gleipnir.instance: "<instance-id>"
    environment:
      # Gleipnir's egress proxy, at this network's gateway address.
      HTTPS_PROXY: "http://172.30.7.1:8118"
      HTTP_PROXY:  "http://172.30.7.1:8118"
      NO_PROXY:    ""       # <- empty on purpose: nothing bypasses the proxy.
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

**Setting `NO_PROXY`.** Each entry is a hole; `NO_PROXY=*` disables egress
containment entirely while every other line still reads correctly.

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
generation rotation, no image GC, no network lifecycle. Automating a lifecycle
action in manual mode would be a contradiction in terms — the whole point is
that the operator owns it.

## Related

- [Container networking](container-networking.md) — the per-instance network model.
- [Egress containment](egress-containment.md) — what the proxy enforces and how.
- [Plugins](../user/plugins.md) — installing, signing, approving.

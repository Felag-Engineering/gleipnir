# Container substrate — operator guide

**Spec:** `docs/developer/mcp-realignment-spec.md` §7, §15 (ADR-056). **Issue:** #820.

The MCP realignment runs plugins as signed containers instead of subprocesses.
This page is the operator's view: which socket Gleipnir talks to and what that
costs you in trust, what address space it needs, and what it will and will not
do to containers it did not create.

> **Status:** the packages below are implemented and tested, but the reconciler
> is not started by `main.go` yet — nothing writes `plugin_containers` rows
> until the manifest-v2 loader work lands. Nothing here is live in a running
> Gleipnir today.

## Socket postures, and what each one costs

Gleipnir picks a posture at startup from `GLEIPNIR_CONTAINER_RUNTIME_MODE`, or
by probing standard socket locations when that is unset.

| Posture | Socket | Trust implication |
|---|---|---|
| `rootless-podman` *(recommended)* | `$XDG_RUNTIME_DIR/podman/podman.sock` | The socket carries **your user's** authority, not root's. A compromise is bounded by what your user can already do. |
| `docker` *(fallback)* | `/var/run/docker.sock` | The socket is **approximately root on the host**. Anyone who can reach it can start a container that mounts `/` and writes to it. |
| `manual` *(escape hatch)* | none written to | You run the containers from your own compose file. Gleipnir discovers by label, health-checks, and **never writes**. |

Auto-detection tries rootless Podman first, then Docker. **Manual is never
auto-selected** — silently disabling writes an operator did not ask to disable
would be worse than failing to start.

### The Docker warning is not boilerplate

Under `docker`, Gleipnir logs a startup WARN saying the socket is approximately
root on the host. That is stated at deploy time rather than buried here because
it is the one decision on this page you cannot undo later without redeploying.

Self-constraint (`container.ValidateCreate`) refuses every hostile create —
bind mounts, `--privileged`, added capabilities, the host network namespace —
**before** the request reaches the socket, and the real-daemon suite asserts
that against an actual runtime, not just against a test double. But
self-constraint bounds what *Gleipnir* sends. It cannot fix a daemon that has
already been compromised by something else, and no socket proxy truly fixes
create-with-hostile-binds either. If rootless Podman is available, use it.

### Manual mode

Full guide, including the reference compose file and the four ways to get it
silently wrong: **[manual-mode.md](manual-mode.md)**.

The short version: `container.ReadOnlyRuntime` makes every write return
`ErrManualModeWrite`, so "Gleipnir never writes" is a property of the type, not
of code remembering to behave.

## Address space

Each plugin instance gets its **own internal network** on its **own `/24`**.
That is what makes a compromised plugin unable to reach a sibling's MCP
endpoint — isolation by topology rather than by policy, which is an ADR-001
guarantee that holds even if the policy layer is wrong.

Subnets therefore are a finite resource, and Gleipnir allocates them explicitly
rather than letting the daemon pick: stock daemon defaults exhaust at roughly
30 networks, and the resulting failure says nothing useful about which pool ran
out or what to do next.

Default pool is `10.83.0.0/16` — 256 instances. Widen it with
`GLEIPNIR_PLUGIN_SUBNET_POOL` if you need more.

### Widen the daemon's own pools too

Gleipnir's allocator and the daemon's allocator do not know about each other.
If you run other containers on the same daemon, tell it to stay out of the same
range — otherwise a Gleipnir instance network and an unrelated compose project
can end up overlapping.

```json
// /etc/docker/daemon.json
{
  "default-address-pools": [
    { "base": "172.30.0.0/16", "size": 24 }
  ]
}
```

Details and the Podman equivalent: **[container-networking.md](container-networking.md)**.

## Egress

Instance networks are created `Internal: true`, so the default is **deny**: a
plugin container has no route off its own network until something deliberately
gives it one. That something is the host-side CONNECT proxy, which matches the
hostnames an admin consented to in the plugin's manifest.

Design record, including why a firewall or DNS filter was rejected:
**[egress-containment.md](egress-containment.md)**.

## Reclamation

Unreferenced images, orphaned subnets, and long-revoked generation tokens are
reclaimed by a separate, slower-cadence pass. What protects a live plugin from
it — and why a *draining* generation still holds its image — is in
**[container-gc.md](container-gc.md)**.

## Environment variables

Added or relevant to this milestone. The full table is in the root `CLAUDE.md`.

| Variable | Default | Description |
|---|---|---|
| `GLEIPNIR_CONTAINER_RUNTIME_MODE` | `auto` | `auto`, `rootless-podman`, `docker`, or `manual`. `auto` probes standard socket locations, preferring rootless Podman. |
| `GLEIPNIR_CONTAINER_SOCKET` | *(unset)* | Explicit socket path, replacing the standard-location probe. Only meaningful with an explicit (non-`auto`) mode. |
| `GLEIPNIR_PLUGIN_SUBNET_POOL` | `10.83.0.0/16` | Base CIDR carved into one `/24` per instance. IPv4, no longer than a `/24`. |
| `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS` | `false` | Dev escape hatch. Signed plugins are still fully verified. |
| `GLEIPNIR_PLUGINS_DIR` | `/plugins` | Watched for plugin bundles. |

## The real-daemon test suite

Everything else in this milestone tests against `container.Fake`, which runs the
**same** `ValidateCreate` / `ValidateCreateNetwork` the real runtime does — so a
Fake-based assertion about a rejection is an assertion about the actual rule.

What a Fake cannot answer is whether the *daemon* agrees:

- does a network created `Internal: true` really have no route out?
- is a subnet the allocator carved one the daemon accepts?
- is an image GC believes is unreferenced one the daemon will actually delete —
  and does it refuse when a container still holds it?
- does a container on one instance network genuinely fail to reach another's?

`internal/plugin/substrate` answers those against a real runtime.

### Running it

```bash
# The suite runs one image, which has to be built rather than pulled.
podman build -t localhost/gleipnir-substrate:latest -f - <<'EOF'
FROM docker.io/library/busybox:latest
CMD ["sleep", "infinity"]
EOF

go test -tags substrate -count=1 ./internal/plugin/substrate/
```

Three requirements, and no stock tag meets all of them:

1. **It must stay running under its default command.** The reconciler creates
   instance containers from a desired-state row, which carries no command
   override — so the image's own `CMD` is what runs, and the planner correctly
   restarts an exited container while the row says `running`. A stock busybox
   (`CMD ["sh"]`, which exits immediately without a TTY) therefore *flaps*
   rather than converging.
2. **It must carry `nc`**, because the isolation probe is a container dialing
   across a network boundary.
3. **It must be local.** An instance network is `Internal`, so a container on
   one cannot pull; a suite that depended on a pull would be testing the
   runner's network instead of the substrate.

Override with `GLEIPNIR_SUBSTRATE_PROBE_IMAGE` if you have something that fits.

### Why a build tag rather than a runtime skip

A `t.Skip()` when no socket is present reads as a **passing test** in every log
that only counts failures. The one suite whose entire job is "we checked against
a real daemon" would then be silently absent exactly where nobody is watching. A
build tag makes running it a decision someone made.

### `make ci-local` never runs it

The lane needs a working socket and a pre-pulled image, and it creates real
containers and networks on your machine. Making the local gate depend on that
would fail `make ci-local` for reasons unrelated to your diff.

So it is CI's — and the local gate **says so** rather than staying quiet about a
lane it did not run:

```
  substrate:   CI-only — this diff reaches it; run 'go test -tags substrate ...'
```

### When CI runs it

Required on PRs that reach the substrate packages, and scheduled nightly
otherwise. Without the schedule, a week where nobody touched those packages
would be a week nobody checked the substrate still works against a real runtime
— and drift there comes from the runtime changing underneath us, not only from
our own diffs.

"Does this diff reach the substrate" is answered by `scripts/ci-local-scope.sh`
— the *same* scoper the local gate uses — rather than by a `paths:` filter
maintained separately in the workflow. A second list would drift from the first,
and the failure mode of that drift is a lane that silently stops running for the
packages it exists to cover. `scripts/ci-local-scope-self-test.sh` pins the
reachability rules, including that `egress` and `resources` select the lane even
though the suite does not import them.

The job is bounded at **15 minutes**. The value is a signal that the substrate
still works against a real runtime, not breadth — breadth belongs in the unit
suites, which are seconds. A job that grew past this budget is one people start
skipping.

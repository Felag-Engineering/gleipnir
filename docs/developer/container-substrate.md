# Container substrate — operator guide

**Spec:** `docs/developer/mcp-realignment-spec.md` §7, §15 (ADR-056). **Issue:** #820.

The MCP realignment runs plugins as signed containers instead of subprocesses.
This page is the operator's view: which socket Gleipnir talks to and what that
costs you in trust, what address space it needs, and what it will and will not
do to containers it did not create.

> **Status:** the packages below are implemented and tested, and
> `internal/plugin/assembly` starts the reconciler — but only in a
> `-tags substratev2` build. The default build never starts it, so nothing here
> runs in a default Gleipnir. The v2 build installs a bundle and approves it through the admin Approve handler.

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

## Which image a container is created from

The manifest pins an image by its config digest, but Docker reads
`repo@sha256:<config digest>` as a registry manifest digest, and a daemon on the
containerd image store (Docker 29's default) resolves neither that nor the bare
config digest for a loaded image. The installer therefore records the local
image ID the daemon reported for the image it verified against the signed
archive, and the reconciler creates every container from that ID. If the image
is gone (for example after a `docker image prune`) the instance is not created
and the reconciler logs why; nothing is pulled. Reinstalling the bundle loads
and records it again. Installs that predate the recorded ID work on the classic
store and Podman and need a reinstall on the containerd store.

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

Four triggers (issue #982):

- **Pull requests** that reach the substrate packages, gated by `substrate-scope`.
- **Pushes to `main`** — a merge is already-landed code, and it's the one point
  every change passes through regardless of which PR carried it.
- **Tag pushes** (release tags) — a release build gets the same real-daemon
  check a merge does.
- **Nightly, on schedule.** Without it, a week where nobody touched those
  packages would be a week nobody checked the substrate still works against a
  real runtime — and drift there comes from the runtime changing underneath us,
  not only from our own diffs.

"Does this diff reach the substrate" is answered by `scripts/ci-local-scope.sh`
— the *same* scoper the local gate uses — rather than by a `paths:` filter
maintained separately in the workflow. A second list would drift from the first,
and the failure mode of that drift is a lane that silently stops running for the
packages it exists to cover. On a push or tag there is no PR diff to scope
against — and the code is already merged — so `substrate-scope` skips the diff
entirely and answers "run" unconditionally for those events. `substrate-scope`
lists reachable packages by directory prefix, not purely by import graph:
`egress` and `resources` select the lane even though the suite does not import
either, and `loader`, `hostendpoint` and `internal/plugin/assembly` are
forward-provisioned ahead of the reconciler wiring that will make them real
dependencies. `scripts/ci-local-scope-self-test.sh` pins one case per prefix.

The job is bounded at **15 minutes**. The value is a signal that the substrate
still works against a real runtime, not breadth — breadth belongs in the unit
suites, which are seconds. A job that grew past this budget is one people start
skipping.

### Nightly failures get a paper trail, not just a red run

A scheduled failure has no PR and no author waiting on a check, so a red run in
the Actions tab is easy to miss. On a nightly failure, CI opens (or, if one is
already open, comments on) a single pinned tracking issue titled
`substrate-integration: nightly failure tracking`, rather than filing a new
issue per failed night. Close it once a nightly run goes green again.

### Required-check status (owner action)

Making `substrate-integration` (and `Build (substratev2)`, and `dood` once it
exists) a **required check** is a repository ruleset setting only the repo
owner can make — it is not something this workflow file can express. As of
this writing that setting has not been made; until it is, a PR can merge
without the substrate lane having run, and the schedule/push/tag triggers above
are what keep the lane exercised in the meantime.

## The DooD suite and its ntfy case

`internal/plugin/dood` (build tags `dood substratev2`) boots the real v2
assembly inside a runner container that has the runtime socket mounted, then
drives signed bundles through install, convergence, calls and teardown. Where
the suite above proves the runtime primitives, this proves the composed system.
`scripts/run-dood-suite.sh` is the one way CI and developers run it, and CI's
`dood` job runs it on PRs that reach it (the same scoper, `dood` output), on
pushes and tags, and nightly.

`TestDooD_NtfyNotifyEndToEnd` (`dood_ntfy_test.go`, #987) installs the real
`plugins/ntfy` bundle and sends it a notification through `hitl.Notifier`:

- the bundle is built by `make ntfy-bundle` (`scripts/build-ntfy-bundle.sh`),
  signed with an ephemeral key and written to `dist/ntfy.tgz`; the harness
  installs it through the production installer (TOFU pin, image digest check)
  and approves it through the admin Approve handler;
- two instances are created; the live one has `server_url: https://ntfy.sh`.
  `server/discover` must declare `io.gleipnir/channel`. The second instance is
  then deactivated, so its entry fails deterministically (a killed container
  would be restarted by the reconciler mid-assertion);
- `Notifier.Notify` fans out to both entries. The test asserts that the
  stopped instance's entry is reported and recorded as a `notify_failed` audit
  event, that the live entry fails for no host-side reason, and that the plugin
  handled the call: `host/get_instance_config` and `host/log` reached the host
  endpoint authenticated as generation 1 of the live instance. That identity
  comes from the assembly's `WithHostCallObserver` option, which reports each
  authenticated host request after token resolution;
- egress: `Proxy.Counters()` must show an allowed tunnel (to `ntfy.sh`, the
  only grant), and a `CONNECT undeclared.example:443` sent from a probe
  container on the instance's own network must get a 403, count as a
  `not_granted` denial and leave a `plugin_egress_denied` audit event. The
  proxy identifies the caller by the address the connection arrived on, so the
  probe holds exactly the live instance's grants. The ntfy image is
  distroless, with no shell to issue the CONNECT from;
- teardown deletes both instances and waits for the managed rows, both
  listeners (host endpoint and egress proxy) and the containers to be gone.
  The harness's label cleanup then has nothing to remove.

Whether ntfy.sh accepted the message is deliberately **not** asserted. The
egress proxy refuses private-range targets, so a local stand-in is impossible
without a test-only dial override, which would be a security regression; the
public service is not ours to depend on. The case does need DNS and outbound
HTTPS to `ntfy.sh` from the runner, because the allow is only counted once the
proxy has dialled the upstream. A sandbox without name resolution fails that
one assertion (`egress allowed = 0`) and nothing else.

### Running it locally

```bash
make conformance-stub-bundle ntfy-bundle   # dist/*.tgz; needs docker or podman
scripts/run-dood-suite.sh                  # the whole suite
DOOD_TEST_ARGS=-test.run=Ntfy scripts/run-dood-suite.sh   # just this case
```

`NTFY_DOCKERFILE`, `NTFY_BUILD_ARGS` and `NTFY_DIST_DIR` override the image
build and output location (see the script header).

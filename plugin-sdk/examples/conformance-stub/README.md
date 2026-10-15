# conformance-stub

A configurable stub plugin for exercising a host's conformance checks. One
container image presents as any combination of the `tool_provider`,
`event_source` and `human_channel` profiles, chosen at startup, and can answer
its own channel requests so a run needs no human.

It is a fixture, not a template: for a first plugin start with
`minimal-tool/` or `minimal-event-source/`.

## What it serves

An `mcpserver` on `:8080`, composed from the profiles in `STUB_PROFILES`:

| Profile | Serves |
|---------|--------|
| `tool_provider` | One tool, `conformance.echo`, which returns its `message` argument. |
| `event_source` | `plugin-sdk/events.Handler` (discover + `events/listen`) over a durable file store in `$STUB_DATA_DIR/events`. One kind, `conformance.tick`, with a string `label` binding field (operators `equals`, `contains`). Events are emitted from configuration, not by a tool. |
| `human_channel` | `plugin-sdk/channelext` with `channel/notify` (logged and accepted) and `channel/request` backed by a file task store in `$STUB_DATA_DIR/tasks`. |

A profile that is off serves nothing of it: event-only mode registers no tools
and declares no `tool_provider`.

## Configuration

All configuration is environment variables, parsed once at startup. A bad value
fails the start with an error naming every offending variable.

| Variable | Default | Meaning |
|----------|---------|---------|
| `STUB_PROFILES` | `tool_provider,event_source,human_channel` | Comma-separated subset of the three profiles. |
| `STUB_EMIT_COUNT` | `3` | Number of `conformance.tick` events to emit after startup (`0` = none). Labels are `tick-1`, `tick-2`, ... |
| `STUB_EMIT_INTERVAL_MS` | `1000` | Wait before each emitted event. |
| `STUB_ASSURANCE` | `authenticated` | `authenticated` or `weak`; the assurance the channel declares. Must agree with the manifest in use. |
| `STUB_AUTO_RESOLVE` | `off` | `off`, `first_option` or `reject`; see below. Needs `human_channel`. |
| `STUB_AUTO_RESOLVE_DELAY_MS` | `0` | Wait between a request arriving and the stub answering it. |
| `STUB_ACTOR_ID` | `conformance-actor` | The external actor id the stub claims to be when it answers. |
| `STUB_DATA_DIR` | `/data` | Where the event and task stores live. |
| `GLEIPNIR_HOST_ENDPOINT_URL`, `GLEIPNIR_INSTANCE_TOKEN` | set by the host | Used by `hostclient`; required only when `STUB_AUTO_RESOLVE` is not `off`. |

### Auto-resolve

When enabled, the stub answers a `channel/request` after the configured delay,
in the background, so `channel/request` itself returns a `working` task at once.

Before it completes anything it calls `host/authorize_actor` with the task id and
`STUB_ACTOR_ID`. Only an authorized result completes the task, with
`actorExternalId` set to that id. If the actor is unauthorized, or the call
fails, the task stays `working`: the contract says the request stays open for a
legitimate actor. A task cancelled in the meantime stays cancelled; the task
store refuses to complete a terminal task.

- `first_option`: answers with the first option; for a schema-only request,
  with an empty content object.
- `reject`: the channel contract has no rejecting resolution, only option ids
  and content, so there is nothing to send that means "no" in itself. The stub
  answers with the option whose id is `reject`, `deny` or `decline`
  (case-insensitive). If the request offers no such option, or offers only a
  schema, the stub does not guess: it leaves the task `working` and does not
  call `authorize_actor`.

## Manifests

`manifests/` holds one canonical manifest per mode. Run the image with the
matching `STUB_PROFILES` (and `STUB_ASSURANCE`):

| Manifest | `STUB_PROFILES` | `STUB_ASSURANCE` |
|----------|-----------------|------------------|
| `all.yaml` | `tool_provider,event_source,human_channel` | `authenticated` |
| `tool-only.yaml` | `tool_provider` | n/a |
| `event-only.yaml` | `event_source` | n/a |
| `human-channel.yaml` | `human_channel` | `authenticated` |
| `human-channel-weak.yaml` | `human_channel` | `weak` |

`manifest.go` is the source of truth; the YAML is its canonical projection.
After editing it:

```sh
go test -run TestManifestsAreCanonical -update .
```

The image digest in each manifest is a placeholder; pin the digest of your own
build.

## Layout

```
conformance-stub/
  main.go          — event kinds, echo tool, composition of the server, emission loop, shutdown
  config.go        — STUB_* parsing (pure function, table-tested)
  channel.go       — channel/notify, the auto-resolver
  manifest.go      — the manifest modes and their projection to manifestv2
  manifests/*.yaml — canonical manifests, one per mode
  Dockerfile       — static, distroless, non-root
```

## Test

```sh
go test -race ./plugin-sdk/examples/conformance-stub/...
```

`manifest_test.go` starts a server per mode and checks that its profiles,
tools, event kinds and channel assurance equal what that mode's manifest
attests. `channel_test.go` drives the auto-resolver against
`hostclient/hostclienttest`. Delays are injected, so no test sleeps.

## Build and run the image

The build context must be the repository root (the module resolves `plugin-sdk`
through a workspace):

```sh
docker build -f plugin-sdk/examples/conformance-stub/Dockerfile -t conformance-stub .
docker run --rm -p 8080:8080 -e STUB_PROFILES=event_source conformance-stub
```

The image is `CGO_ENABLED=0` on `gcr.io/distroless/static-debian12:nonroot`, runs
as uid 65532, and with no arguments serves until it receives SIGTERM. `/data` is
writable by that user; mount a volume there to keep events and tasks across
restarts.

## Signed bundles

```sh
make conformance-stub-bundle
```

builds the image (docker, else podman; override with `CONTAINER_ENGINE`) and
writes one signed v2 bundle per manifest to `dist/conformance-stub-<mode>.tgz`.
The script (`scripts/build-conformance-stub.sh`) stamps each bundle's manifest
with the saved image's real config digest and a bundle-safe name
(`conformance-stub-<mode>`; the registry-style `io.example/...` name in
`manifests/` is not a valid bundle path component), then signs with a throwaway
keypair that lives in a temp directory and is deleted on exit. No key is
written to the work tree, and the committed manifests are not modified.

Only the bundle build touches a registry (the Dockerfile's base images). Each
bundle carries its own image archive, so a host that installs it
(`docker load`) never pulls. Smoke check:

```sh
go test -tags conformance_bundle -run TestConformanceStubBundles ./internal/plugin/loader
```

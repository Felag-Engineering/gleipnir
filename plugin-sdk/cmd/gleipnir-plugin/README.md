# gleipnir-plugin CLI

Developer CLI for Gleipnir plugin authors. Decoupled from `gleipnirctl` (which
is a server-admin CLI).

## Building

```bash
cd plugin-sdk
go build ./cmd/gleipnir-plugin
```

## Subcommands

| Command | Purpose | Issue |
|---------|---------|-------|
| `new` | Scaffold a new plugin | #169 |
| `validate` | Validate manifest + binary (v1) or manifest alone (v2) | #169, #970 |
| `gen-manifest` | Emit deterministic manifest YAML (v1 only) | #169 |
| `keygen` | Generate a Minisign keypair | #170 |
| `sign` | Sign a binary + manifest | #170 |
| `package` | Build, sign, and tar a release bundle (v1 binary or v2 OCI image) | #170, #970 |
| `run` | Non-interactive dev mode against a fake host (scenario/capture/replay) | #171 |

## Usage

### `gleipnir-plugin new <name>`

Scaffold a new plugin project:

```bash
gleipnir-plugin new myplugin
gleipnir-plugin new myplugin --kind channel
gleipnir-plugin new myplugin --kind combo --module github.com/myorg/myplugin
```

`--kind` variants:
- `tool` (default) — ToolService with one example tool
- `channel` — ChannelService with Notify + Request stubs
- `trigger` — TriggerService with one EmitEvent example
- `combo` — all three services

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--kind` | `tool` | Plugin kind: `tool`, `channel`, `trigger`, `combo` |
| `--dir` | `./<name>` | Output directory |
| `--module` | `example.com/<name>` | Go module path written to `go.mod` |
| `--sdk-replace` | (none) | Local filesystem path to `plugin-sdk`; adds a `replace` directive to `go.mod` (local dev only) |

Each scaffold writes `main.go`, `manifest.go`, `service.go`, `service_test.go`
(kind-specific), plus `go.mod`, `Makefile`, `manifest.yaml`, `README.md`, and
`.gitignore`.

### `gleipnir-plugin gen-manifest`

**v1 (gRPC-subprocess) manifests only.** A v2 (containerized) manifest has no
generating binary to invoke — see [`v2 (containerized) packaging`](#v2-containerized-packaging)
below — and `gen-manifest` does not apply to it. #1009 removes this command
when v1 `--binary` packaging is deleted.

Invoke `<binary> --emit-manifest` and write canonical YAML. `--out` writes to a
file; when omitted, the YAML is written to stdout:

```bash
go build -o myplugin .
gleipnir-plugin gen-manifest --binary ./myplugin --out manifest.yaml
```

The canonical YAML has sorted keys and 2-space indent. Re-running for the same
Go declarations produces byte-identical output (required for signing).

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--binary` | (required) | Path to the plugin binary |
| `--out` | (stdout) | Output file path |

### `gleipnir-plugin validate`

For a v1 manifest, checks that `manifest.yaml` matches the binary's current
declarations:

```bash
gleipnir-plugin validate --binary ./myplugin --manifest manifest.yaml
```

Exits 0 on match, 1 with a diff on mismatch. Run `gen-manifest` to fix drift.

For a v2 (containerized) manifest, `--binary` is omitted — there is no binary
to compare against — and `validate` instead parses and validates the manifest
against every rule the host will enforce at install time (profiles, egress,
resources, auth, tier2):

```bash
gleipnir-plugin validate --manifest manifest.yaml
```

`validate` picks the mode from the manifest's own `schema_version` field; no
flag selects it.

### `gleipnir-plugin keygen`

Generate a Minisign-compatible Ed25519 signing keypair:

```bash
gleipnir-plugin keygen
gleipnir-plugin keygen --out-dir ./keys --name myplugin
gleipnir-plugin keygen --kdf argon2   # requires minisign >= 0.11 on operator side
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--out-dir` | `~/.config/gleipnir-plugin/keys/` | Output directory |
| `--name` | `signing` | Base filename (`<name>.key`, `<name>.pub`) |
| `--kdf` | `scrypt` | KDF: `scrypt` (default) or `argon2` |
| `--force` | false | Overwrite existing key files |
| `--passphrase-stdin` | false | Read passphrase from stdin (CI) |
| `--unencrypted` | false | Skip passphrase (testing only) |

**KDF notes:**
- `scrypt` is the default and works with all `minisign` versions.
- `argon2` (`Ar`) requires upstream `minisign >= 0.11` (2023). Use when both the
  key generator and operator's `minisign` tool are known to be >= 0.11.

**CI passphrase:** Set `GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE` env var, or use
`--passphrase-stdin`.

### `gleipnir-plugin sign`

Sign a plugin binary + manifest:

```bash
gleipnir-plugin sign --binary ./myplugin --manifest manifest.yaml
gleipnir-plugin sign --binary ./myplugin --manifest manifest.yaml \
    --key ./keys/signing.key --out myplugin.minisig
```

The signed payload is `sha256(binary) || sha256(manifest)` per spec §5.2.
The `.minisig` defaults to `<binary-basename>.minisig` in the current directory.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--binary` | (required) | Path to the plugin binary |
| `--manifest` | `manifest.yaml` | Path to manifest.yaml |
| `--key` | `~/.config/gleipnir-plugin/keys/signing.key` | Secret key path |
| `--key-stdin` | false | Read .key from stdin (CI) |
| `--out` | `<binary-basename>.minisig` | Output `.minisig` path |
| `--trusted-comment` | (timestamp + manifest name/version) | Minisign trusted comment |

**Key resolution order:**
1. `--key-stdin` — read .key content from stdin
2. `GLEIPNIR_PLUGIN_SIGNING_KEY` env var — path or inline .key content
3. `--key` flag
4. `~/.config/gleipnir-plugin/keys/signing.key`

**Passphrase resolution order:**
1. `GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE` env var
2. Interactive terminal prompt

### `gleipnir-plugin package`

The manifest's `schema_version` picks the mode: v1 (`--binary`, a
gRPC-subprocess plugin) or v2 (`--image-archive`/`--image`, a containerized
plugin). Supplying the wrong flag for the manifest's version is an error —
`--binary` and `--image-archive`/`--image` are mutually exclusive.

**v1 (gRPC-subprocess) packaging:**

```bash
gleipnir-plugin package --binary ./myplugin
gleipnir-plugin package --binary ./myplugin --manifest manifest.yaml \
    --key ./keys/signing.key --pubkey ./keys/signing.pub \
    --out-dir ./dist
gleipnir-plugin package --binary ./myplugin --sbom sbom.cyclonedx.json
```

**Bundle layout** (spec §14.5):

```
<name>-<version>.tar.gz
  <name>-<version>/
    <manifest.Name>           (mode 0755, the binary)
    manifest.yaml             (mode 0644)
    <manifest.Name>.minisig   (mode 0644)
    signing.pub               (mode 0644)
    sbom.cyclonedx.json       (mode 0644, optional)
```

Both the binary and the `.minisig` filename derive from `manifest.Name`, not the
source binary's basename — the host locates the binary at `<bundle>/<manifest.Name>`
to hash and verify it.

#### v2 (containerized) packaging

A `schema_version: "2"` manifest packages a container image instead of a
binary. Exactly one of `--image-archive`/`--image` is required:

```bash
# From a pre-saved archive (docker save / podman save, either OCI or Docker
# legacy layout):
gleipnir-plugin package --manifest manifest.yaml --image-archive image.tar \
    --key ./keys/signing.key --pubkey ./keys/signing.pub

# Or let the CLI save the image itself (shells out to docker, then podman):
gleipnir-plugin package --manifest manifest.yaml --image ghcr.io/acme/myplugin@sha256:...
```

**Package-time digest check:** before anything is signed, the archive is read
once into memory and its image config digest — the classic image ID (what
`docker inspect --format '{{.Id}}'` reports), *not* the manifest digest or a
registry repo digest — is computed from those same bytes via
[`plugin-sdk/imagearchive`](../../imagearchive) and compared against the
manifest's `package.identifier` digest pin. A mismatch fails the command,
naming both digests, rather than producing a bundle `internal/plugin/loader`'s
`OCIInstaller` would reject at install time — the same comparison it makes
after loading the image into the runtime (spec §7). Every digest
`imagearchive` reports is recomputed from the referenced bytes and hashed, not
trusted from a JSON field: an OCI-layout manifest blob is checked against
`index.json`'s own descriptor digest, and the config blob it names is checked
against that digest, before either is believed.

`imagearchive` is its own exported `plugin-sdk` package, not CLI-internal
code, specifically so the host's install-time verification can import the
exact same computation (#1032) instead of re-deriving it — the package-time
and install-time checks must never become two implementations that could
quietly drift apart.

> **containerd-image-store caveat (tracked in #1032, not solved here):** the
> digest this command checks is the config digest read out of the *archive*.
> A container engine using the containerd image store may report a different
> value (e.g. a manifest or index digest) as a loaded image's own ID for some
> archive shapes. The host-side comparison against `package.identifier` — done
> after loading the image into whatever runtime is configured, not from the
> archive — needs to account for that; this package-time check does not
> attempt to, since it only ever looks at the archive itself.

`gleipnir-plugin package` also warns (without failing the build) when:
- the archive is tagged with a repository other than the manifest's own
  (`RepoTags` for a Docker-legacy archive, or the OCI manifest descriptor's
  `org.opencontainers.image.ref.name` annotation) — a possible sign of
  packaging the wrong image; and
- the finished bundle's total uncompressed size exceeds the host's tarball
  extraction cap (100 MiB, `internal/plugin/loader/extract.go`) — the bundle
  will build, but the host will refuse to extract it.

**Bundle layout** (must match `ociManifestFilename`/`ociImageArchiveName` in
`internal/plugin/loader/ocibundle.go` — the SDK cannot import `internal/*` to
share those constants):

```
<name>-<version>.tar.gz
  <name>-<version>/
    image.tar                 (mode 0644, the OCI/Docker image archive)
    manifest.yaml             (mode 0644)
    <manifest.Name>.minisig   (mode 0644)
    signing.pub               (mode 0644)
    sbom.cyclonedx.json       (mode 0644, optional)
```

The signed payload is `sha256(image.tar) || sha256(manifest.yaml)` — the same
`sha256(artifact) || sha256(manifest)` shape v1 uses, with the image archive
standing in for the binary. `--unsigned` behaves exactly as it does for v1.

**Unsigned bundles:**

Use `--unsigned` to produce a bundle without `.minisig`/`signing.pub`. The host
must have `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true` set to load it; a red banner
appears in the admin UI and audit events are logged on every load. Even in
permissive mode, signed plugins are fully verified. See spec §5.5.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--binary` | (none) | Path to plugin binary (v1 manifests only) |
| `--manifest` | `manifest.yaml` | Path to manifest.yaml |
| `--image-archive` | (none) | Path to a pre-saved image archive (v2 manifests only) |
| `--image` | (none) | Image reference to save via docker/podman (v2 manifests only) |
| `--key` | `~/.config/gleipnir-plugin/keys/signing.key` | Secret key path |
| `--key-stdin` | false | Read .key from stdin (CI) |
| `--pubkey` | sibling of .key | Public key path for bundle |
| `--out-dir` | `./dist` | Output directory |
| `--sbom` | (none) | CycloneDX SBOM JSON path |
| `--unsigned` | false | Produce unsigned bundle |

**Deterministic tarballs:** Entry order is sorted; `SOURCE_DATE_EPOCH` env var
sets the mtime for reproducible builds.

**`--image` hardening:** a ref starting with `-` is rejected outright (it
would otherwise be shelled out to `docker`/`podman save` as an argument this
CLI does not further inspect); the ref is additionally passed after a `--`
argument so both tools treat it as positional even if some future ref shape
slips past that check.

## Environment variables

| Variable | Description |
|----------|-------------|
| `GLEIPNIR_PLUGIN_SIGNING_KEY` | Path to `.key` file, or inline `.key` content |
| `GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE` | Passphrase for the signing key |

## Interop verification (AC#7)

The in-process format-shape test runs unconditionally:

```bash
go test ./plugin-sdk/signing/...
```

Full upstream-CLI verification (requires `minisign` binary on PATH):

```bash
go test -tags integration ./plugin-sdk/signing/...
```

## run

Non-interactive local development mode. Boots a plugin binary as a go-plugin
subprocess connected to an in-process fake host. No REPL/TUI mode — the three
batch modes below cover all automation-friendly workflows.

```bash
gleipnir-plugin run <binary> --scenario script.yaml
gleipnir-plugin run <binary> --capture events.jsonl [--max-events N] [--watch-scope JSON]
gleipnir-plugin run <binary> --replay events.jsonl [--filter event_kind=X] [--continue-on-error]
```

### Scenario YAML schema

```yaml
steps:
  # Negotiate the handshake with the plugin.
  - rpc: Handshake.Negotiate
    request:
      host_version: "0.0.0-dev"
      expected_capabilities: []   # optional; omit to accept any
    assert_response:
      ok: true                    # bool
      sdk_version: "1.0.0"       # optional string equality

  # Verify the plugin exposes at least N tools.
  - rpc: Tool.ListTools
    request: {}
    assert_response:
      min_tools: 1                # minimum tool count

  # Call a named tool and assert the output contains a substring.
  - rpc: Tool.Call
    request:
      tool_name: echo
      input_json: '{"text":"hello"}'
    assert_response:
      result_contains: "hello"    # substring match against output_json

  # Assert on fake-host recorder state after preceding steps.
  - assert_host:
      min_events: 1
      min_metrics: 0
      min_logs: 0
```

`KnownFields(true)` is enforced: unknown field names in the YAML cause an
immediate error rather than silent no-ops.

### Capture JSONL format

`--capture <file.jsonl>` writes:

**Header line** (always first, sequence = -1):
```json
{"sequence":-1,"capture_format_version":1,"binary":"./myplugin","captured_at":"RFC3339Nano"}
```

**Event line** (one per EmitEvent call the plugin makes):
```json
{"captured_at":"RFC3339Nano","sequence":1,"event_id":"...","event_kind":"github.push","payload_json":"{...}","watch_scope_json":"{...}"}
```

The format is stable across minor SDK versions; readers must check
`capture_format_version == 1` before processing.

### `--replay-event` convention

For `--replay` to work, your plugin binary must implement a `--replay-event`
flag. When invoked with that flag the host **pipes the JSON event payload to
the plugin's stdin** — the plugin must call `io.ReadAll(os.Stdin)` to receive
it. The payload is never passed as a CLI argument: Linux `ARG_MAX` (~2MB)
would silently fail for large webhook payloads, which can reach the 16MB JSONL
scanner limit.

The plugin should:

1. Read the full event JSON from stdin with `io.ReadAll(os.Stdin)`.
2. Parse it (same shape as an `EmitEventRequest` payload).
3. Process it as if received from the real substrate.
4. Exit 0 on success, non-zero on failure.

Example in a plugin `main.go`:

```go
if len(os.Args) >= 2 && os.Args[1] == "--replay-event" {
    raw, err := io.ReadAll(os.Stdin)
    if err != nil {
        fmt.Fprintln(os.Stderr, "replay-event: read stdin:", err)
        os.Exit(1)
    }
    var evt map[string]interface{}
    if err := json.Unmarshal(raw, &evt); err != nil {
        fmt.Fprintln(os.Stderr, "bad event JSON:", err)
        os.Exit(1)
    }
    // process evt...
    os.Exit(0)
}
```

Plugins that do not implement this flag will produce non-zero exits during
`--replay`, which the runner reports as FAILED. This is expected and harmless
for plugins that do not need offline payload iteration.

### `WriteAuditStep` step_type contract

The fake host (and the production host) enforce that `step_type` must be
`"feedback_response"` in v1. Any other value returns:

- gRPC status code: `codes.PermissionDenied`
- Detail string: `"unauthorized_step_type"`

Production host implementations MUST mirror this contract exactly so plugin
authors can rely on consistent behavior between local dev and production.

### Non-goals

- No interactive REPL / TUI mode (explicitly out of scope for #171).
- Does NOT simulate signature verification, version mismatch, or a real
  LLM/SQLite.
- Host configuration does not affect it — `gleipnir-plugin run` runs
  out-of-band, not through the host's plugin loader.

See `docs/developer/plugin-system-spec.md §14.4` for the testing harness spec
and `§7.5` for the trigger-payload sharp edge that capture/replay addresses.

## See also

`docs/developer/plugin-system-spec.md §14.5` for the full v1 subcommand
reference and bundle layout, and `§5.2` for the signing scheme.
`docs/developer/mcp-realignment-spec.md §7` for the v2 (containerized) bundle
format `package`/`validate` implement.

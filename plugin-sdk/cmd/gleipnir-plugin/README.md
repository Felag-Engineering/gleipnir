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
| `validate` | Validate manifest + binary | #169 |
| `gen-manifest` | Emit deterministic manifest YAML | #169 |
| `keygen` | Generate a Minisign keypair | #170 |
| `sign` | Sign a binary + manifest | #170 |
| `package` | Build, sign, and tar a release bundle | #170 |
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

Check that `manifest.yaml` matches the binary's current declarations:

```bash
gleipnir-plugin validate --binary ./myplugin --manifest manifest.yaml
```

Exits 0 on match, 1 with a diff on mismatch. Run `gen-manifest` to fix drift.

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

Build a signed release tarball:

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

**Unsigned bundles:**

Use `--unsigned` to produce a bundle without `.minisig`/`signing.pub`. The host
must have `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true` set to load it; a red banner
appears in the admin UI and audit events are logged on every load. Even in
permissive mode, signed plugins are fully verified. See spec §5.5.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--binary` | (required) | Path to plugin binary |
| `--manifest` | `manifest.yaml` | Path to manifest.yaml |
| `--key` | `~/.config/gleipnir-plugin/keys/signing.key` | Secret key path |
| `--key-stdin` | false | Read .key from stdin (CI) |
| `--pubkey` | sibling of .key | Public key path for bundle |
| `--out-dir` | `./dist` | Output directory |
| `--sbom` | (none) | CycloneDX SBOM JSON path |
| `--unsigned` | false | Produce unsigned bundle |

**Deterministic tarballs:** Entry order is sorted; `SOURCE_DATE_EPOCH` env var
sets the mtime for reproducible builds.

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
      min_audit_steps: 0
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

`docs/developer/plugin-system-spec.md §14.5` for the full subcommand reference
and bundle layout. `docs/developer/plugin-system-spec.md §5.2` for the signing
scheme.

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
| `run` | Local REPL/TUI dev mode against a fake host | #171 |

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

### `gleipnir-plugin gen-manifest`

Invoke `<binary> --emit-manifest` and write canonical YAML to `manifest.yaml`:

```bash
go build -o myplugin .
gleipnir-plugin gen-manifest --binary ./myplugin --out manifest.yaml
```

The canonical YAML has sorted keys and 2-space indent. Re-running for the same
Go declarations produces byte-identical output (required for signing).

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
    <binary-basename>         (mode 0755)
    manifest.yaml             (mode 0644)
    <manifest.Name>.minisig   (mode 0644)
    signing.pub               (mode 0644)
    sbom.cyclonedx.json       (mode 0644, optional)
```

The `.minisig` filename derives from `manifest.Name`, not the binary basename.

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

## See also

`docs/developer/plugin-system-spec.md §14.5` for the full subcommand reference
and bundle layout. `docs/developer/plugin-system-spec.md §5.2` for the signing
scheme.

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

## See also

`docs/developer/plugin-system-spec.md §14.2` for the full subcommand reference.

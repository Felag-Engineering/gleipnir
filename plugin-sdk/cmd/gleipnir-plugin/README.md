# gleipnir-plugin CLI

Developer CLI for Gleipnir plugin authors. Decoupled from `gleipnirctl` (which
is a server-admin CLI).

## Building

```bash
cd plugin-sdk
go build ./cmd/gleipnir-plugin
```

## Planned subcommands

| Command | Purpose | Issue |
|---------|---------|-------|
| `new` | Scaffold a new plugin | #169 |
| `validate` | Validate manifest + binary | #169 |
| `keygen` | Generate a Minisign keypair | #170 |
| `sign` | Sign a binary + manifest | #170 |
| `package` | Build, sign, and tar a release bundle | #170 |
| `run` | Local REPL/TUI dev mode against a fake host | #171 |
| `gen-manifest` | Emit deterministic manifest YAML | #169 |

See `docs/developer/plugin-system-spec.md §14.2` for the full specification.

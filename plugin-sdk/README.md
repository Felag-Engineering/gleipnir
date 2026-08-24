# Gleipnir Plugin SDK

The plugin SDK provides the protobuf contracts, generated gRPC stubs, and
developer tooling for building Gleipnir plugins.

> **New to plugin development?** Start with the
> [Plugin Author Guide](../docs/developer/plugin-author-guide.md) — it walks
> through scaffolding, implementing a service, signing, packaging, and
> installing a plugin end to end.

## Structure

```
plugin-sdk/
  proto/          — .proto source files for all plugin services
  gen/            — generated Go stubs (committed; regenerate with `make proto`)
  manifest/       — manifest builder types (code-first manifest authoring)
  serve/          — plugin entry point: serve.Serve() + WithXHandler / WithXService options
  tool/           — tool.Service ergonomic interface (plain-Go tool handlers)
  channel/        — channel.Service ergonomic interface (Notify / Request handlers)
  trigger/        — trigger.Service ergonomic interface (event emit callback)
  credentials/    — typed accessors for the credential strategies
  pluginerr/      — error codes + ErrorEnvelope helpers for plugin handlers
  hostclient/     — typed Go client for the MCP realignment host endpoint (ADR-057 as
                    amended, spec §8); zero-protobuf, stdlib-only (ADR-060 Amendment 1).
                    Reads GLEIPNIR_HOST_ENDPOINT_URL / GLEIPNIR_INSTANCE_TOKEN from the
                    environment by default. Not yet the live path — the v1.1 gRPC plane
                    (hostwire/ + serve/'s TokenInterceptorFromEnv) is what plugins call
                    today, until the #883 cutover.
  hostwire/       — go-plugin handshake config and gRPC wiring shared by serve/
  signing/        — bundled Minisign sign/verify library (ADR-043)
  testing/        — fake host for unit tests (NewFakeHost)
  examples/       — end-to-end examples
  cmd/
    gleipnir-plugin/  — developer CLI (new, gen-manifest, validate, keygen, sign, package, run)
  internal/
    tools/        — pinned generator versions for `go mod tidy`
```

The module path is `github.com/felag-engineering/gleipnir/plugin-sdk` and it
requires Go 1.25 (`go 1.25.11` in `go.mod`).

## Proto services

| Service | Package | Purpose |
|---------|---------|---------|
| `HandshakeService` | `gleipnir.plugin.handshake.v1` | Protocol negotiation — immortal, never bumps |
| `BootstrapService` | `gleipnir.plugin.bootstrap.v1` | Per-instance bind step run after the handshake (additive, evolves independently) |
| `ToolService` | `gleipnir.plugin.tool.v1` | Agent-callable tools (`ListTools`, `Call`) |
| `ChannelService` | `gleipnir.plugin.channel.v1` | Notifications and request/response feedback |
| `TriggerService` | `gleipnir.plugin.trigger.v1` | Long-lived event stream |
| `HostService` | `gleipnir.plugin.host.v1` | Host API (Tier-1 + Tier-2 RPCs) |
| `CommonService` | `gleipnir.plugin.common.v1` | Shared messages (`RequestContext`, `ErrorEnvelope`) |

## Regenerating stubs

```bash
make proto   # runs buf generate with pinned BSR plugins
```

Requires `buf` to be installed. See `buf.gen.yaml` at repo root.

## Full specification

See `docs/developer/plugin-system-spec.md §14` for the complete developer
experience design.

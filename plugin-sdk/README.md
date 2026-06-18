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
  serve/          — plugin entry point: serve.Serve()
  testing/        — fake host for unit tests
  examples/       — end-to-end examples
  cmd/
    gleipnir-plugin/  — developer CLI (new, gen-manifest, validate, keygen, sign, package, run)
  internal/
    tools/        — pinned generator versions for `go mod tidy`
```

## Proto services

| Service | Package | Purpose |
|---------|---------|---------|
| `HandshakeService` | `gleipnir.plugin.handshake.v1` | Protocol negotiation — immortal, never bumps |
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

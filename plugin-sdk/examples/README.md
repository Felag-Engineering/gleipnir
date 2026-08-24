# Plugin SDK Examples

End-to-end examples showing how to build Gleipnir plugins with the SDK.
For the planned structure of each example see
`docs/developer/plugin-system-spec.md §14.7`.

## Shipped

| Directory | What it shows |
|-----------|---------------|
| [`minimal-tool/`](minimal-tool/README.md) | Smallest possible `ToolService` plugin: one `echo` tool, one host RPC each of `GetInstanceConfig` / `EmitMetric` / `Log`. Start here. |
| [`host-client/`](host-client/main.go) | Compile-only example of `plugin-sdk/hostclient`, the typed, zero-protobuf client for the MCP realignment host endpoint (#882): constructing a `Client` and calling a couple of typed methods, plus matching on `*hostclient.HostError` vs `*hostclient.JSONRPCError`. |
| [`minimal-event-source/`](minimal-event-source/README.md) | Smallest possible `io.gleipnir/events` plugin (ADR-054, #904): `plugin-sdk/events.Handler` serving `server/discover` / `events/discover` / `events/listen` for one event kind, `example.ping`, with a `manifestv2` manifest declaring `profiles.event_source` kept in agreement with it by test. |

## Planned (tracked in follow-up issues)

| Directory | What it will show |
|-----------|-------------------|
| `minimal-trigger/` | A `TriggerService` plugin emitting one event kind (the v1.1 gRPC substrate; distinct from `minimal-event-source/`'s MCP-realignment `io.gleipnir/events`) |
| `minimal-channel/` | A `ChannelService` plugin with Notify only |
| `static-api-key/` | A `ToolService` plugin using `static_api_key` credentials |

# Plugin SDK Examples

End-to-end examples will be added in issue #173 once the plugin loader and
`serve.Serve()` implementation land (Phase 3).

Planned examples:
- `minimal-tool/` — the simplest possible ToolService plugin
- `minimal-trigger/` — a TriggerService plugin emitting one event kind
- `minimal-channel/` — a ChannelService plugin with Notify only
- `static-api-key/` — a ToolService plugin using `static_api_key` credentials

For now, see `docs/developer/plugin-system-spec.md §14.7` for the planned
structure of each example.

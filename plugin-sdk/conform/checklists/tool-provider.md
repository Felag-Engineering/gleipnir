# Tool-provider conformance checklist

Checklist version: 1.0.0 · tracks the tool-provider profile 1.0.0
(`docs/developer/profile-tool-provider.md`)

Each item has a stable ID and exactly one mode:

- `auto`: the conformance runner (`plugin-sdk/conform`) checks it against a
  running server with no help.
- `stimulus`: checkable automatically, but only after something outside the
  server makes it act.
- `review`: needs a person reading the plugin.

Each item also says what the **host** does about it today. "None" means the
runner is the only check, and a plugin that fails it still installs.

The ID and mode on each item line are machine-read: the runner's
`checklist_test.go` pins its check registry to this file, so adding, removing or
re-moding an item here without the matching check fails the build.

A plugin that does not declare `tool_provider` runs none of these items.

## Protocol and discovery

- **TP-PROTO-01** `auto` — `server/discover` succeeds and its
  `supportedVersions` includes `2026-07-28`.
  - Host: the managed endpoint is pinned to `2026-07-28` at registration
    (`internal/mcp`). Not checked against the server: a legacy-only server
    still passes liveness (profile doc §6.3).
- **TP-DISC-01** `auto` — `tools/list` succeeds over the stateless modern
  transport (no `initialize`, no session) and returns a well-formed result.
  - Host: `internal/mcp` discovers tools in the attach pass
    (`internal/plugin/assembly`). A failure is logged at WARN and the endpoint
    is kept with no tools.
- **TP-DISC-02** `auto` — Every tool's `inputSchema`, where present, is valid
  JSON (valid UTF-8, no duplicate object keys) and compiles as JSON Schema.
  - Host: `internal/schemanorm` via `internal/mcp` at discovery canonicalizes
    each schema byte-for-byte and enforces its size limits (1 MiB, depth 64,
    10,000 nodes). It is fail-open: a schema it refuses leaves the tool kept,
    with no canonical schema and no argument validation. Nothing on the host
    compiles the schema. Canonicalization itself is not repeated by the
    runner; it stays at install time.
- **TP-DISC-03** `auto` — Tool names are unique within `tools/list`.
  - Host: the `mcp_tools` unique constraint (`internal/db`) fails the whole
    discovery.

## Names

- **TP-NAME-01** `auto` — Every tool name is 1–128 characters from
  `[A-Za-z0-9_.-]`.
  - Host: none.
- **TP-NAME-02** `auto` — No tool name starts with `host/`.
  - Host: `internal/plugin/hostendpoint` (`AssertHostPlane`), once at boot,
    against the host endpoint's own inventory only. It cannot see managed
    plugin tools, which are discovered after it runs.
- **TP-NAME-03** `auto` — No tool name equals an `io.gleipnir/*` method:
  `channel/notify`, `channel/request`, `events/discover`, `events/listen`,
  `events/event`.
  - Host: none.

## Manifest agreement

- **TP-MAN-01** `auto` — Every name in the manifest's `gleipnir.tools` appears
  in `tools/list`.
  - Host: none. Grants are checked against discovered tools, not the manifest
    (`internal/plugin/assembly`, managed tool classifier).
- **TP-MAN-02** `auto` — A server whose `tools/list` is non-empty declares the
  `tool_provider` profile.
  - Host: `internal/plugin/assembly` (managed tool classifier) refuses every
    grant on an instance that does not declare it, at run start.

## Calls

- **TP-CALL-01** `auto` — `tools/call` for a name not in `tools/list` returns a
  JSON-RPC error or an `isError` result, never a success.
  - Host: not checked. The host only calls discovered tools, and
    `internal/execution/agent` records any failure as an `is_error` tool
    result.

## Discipline

- **TP-REV-01** `review` — No tool performs a host-initiated interaction:
  channel delivery, configuration-option lookup or identity-code delivery.
  Those are extension methods (spec §4, "if the host initiates it, it is not a
  tool"), whatever the tool is called.
  - Host: none; names alone cannot show intent.

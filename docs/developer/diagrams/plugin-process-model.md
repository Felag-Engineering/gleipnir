# Plugin Process Model

How the host launches, manages, and communicates with plugin subprocesses. Mirrors `docs/developer/plugin-system-spec.md` §3.1.

## Process topology

```mermaid
graph TB
    subgraph CONTAINER["Gleipnir API container"]
        subgraph HOST["Gleipnir host process (Go)"]
            LIFECYCLE["run lifecycle · audit · scheduling"]
            RESTAPI["admin UI · REST API · SSE"]
            PLUGMGR["plugin manager"]
            DISPATCH["dispatch pool"]
        end

        subgraph PLUGINS["Plugin subprocesses"]
            SP1["<b>plugin subprocess</b><br/>e.g. slack-prod<br/>(gRPC over UDS)"]
            SP2["<b>plugin subprocess</b><br/>e.g. ntfy<br/>(gRPC over UDS)"]
        end

        WATCH[("<b>fsnotify watcher</b><br/>/plugins directory")]
    end

    EXT["<b>MCP server</b><br/>(external, HTTP)"]

    WATCH -->|"tarball drop"| PLUGMGR
    PLUGMGR -->|"spawn"| SP1
    PLUGMGR -->|"spawn"| SP2
    DISPATCH <-->|"gRPC/UDS<br/>per-instance socket"| SP1
    DISPATCH <-->|"gRPC/UDS<br/>per-instance socket"| SP2
    HOST <-->|"HTTP (JSON-RPC)"| EXT
```

> Each plugin instance gets its own Unix domain socket. The dispatch pool holds one gRPC connection per running instance; the MCP client holds HTTP connections to external MCP servers. Both share the same `<source>.<tool>` namespace via `internal/toolregistry`.

## Generation lifecycle

Plugin instances go through a well-defined lifecycle from install to running. Hot-reload replaces a generation without stopping in-flight calls.

```mermaid
stateDiagram-v2
    direction TB

    [*] --> uploaded : tarball dropped into /plugins (fsnotify or admin upload API)

    uploaded --> verified : verify Minisign signature
    verified --> tofu : capture pubkey (TOFU at first install)
    verified --> key_mismatch : pubkey differs from pinned key

    key_mismatch --> blocked_key : block pending admin Accept-new-key
    blocked_key --> tofu : admin approves

    tofu --> snapshotted : snapshot manifest to DB

    snapshotted --> spawned : spawn subprocess
    spawned --> handshake : gRPC handshake (protocol negotiation)
    handshake --> healthy : handshake OK

    healthy --> hot_reload : new tarball detected (fsnotify)
    hot_reload --> material_check : verify + diff manifest
    material_check --> drain : cosmetic change only, drain old generation
    material_check --> blocked_manifest : material change, block pending admin Accept-manifest
    blocked_manifest --> drain : admin approves
    drain --> spawned : spawn new generation (old gen drains in-flight calls)

    healthy --> [*] : uninstall / deactivate
```

> **Generation** — a specific subprocess incarnation of an instance. The old generation continues serving in-flight calls (via `internal/plugin/generation` refcounts) while the new generation starts. Once the old generation's refcount reaches zero it is killed.

## Notes

- **Identity tokens** — each subprocess generation receives a unique `GLEIPNIR_INSTANCE_TOKEN` at spawn. The `hostsvc.UnaryInstanceTokenInterceptor` validates the token on every Host RPC. A killed generation's token is immediately revoked so it cannot impersonate the new generation.
- **Generation refcounts** — `internal/plugin/generation.Controller` tracks in-flight Host RPCs per generation. `BeginDrain` blocks new traffic and waits for old-gen refcount to reach zero before force-cancelling stragglers (5s grace).
- **Host RPCs** — plugins call back to the host via 8 always-on Tier-1 RPCs (`GetInstanceConfig`, `GetCredentials`, `GetRunContext`, `WriteAuditStep`, `EmitMetric`, `EmitEvent`, `Log`, `SetHealthState`) and 2 capability-gated Tier-2 RPCs (`RunHistoryRead`, `UserDirectoryRead`).
- For the full design specification see `docs/developer/plugin-system-spec.md`.

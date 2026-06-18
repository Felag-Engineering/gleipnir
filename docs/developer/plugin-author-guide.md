# Plugin Author Guide (v1.1)

This guide is for external developers building a Gleipnir plugin. It covers the full authoring loop: scaffold, implement, test, sign, package, and install.

## 1. Overview

A Gleipnir plugin is a single Go binary. The host process spawns it as a subprocess, connects over gRPC on a Unix Domain Socket (via HashiCorp go-plugin), and routes calls to whichever of the three capability surfaces the plugin declares:

| Service | What it does |
|---------|-------------|
| **ToolService** | Exposes agent-callable tools (`ListTools`, `Call`). The agent sees them as `<instance>.<tool>` in its tool namespace. |
| **ChannelService** | Delivers notifications (`Notify`) and opens human-in-the-loop feedback channels (`Request`). |
| **TriggerService** | Opens a long-lived stream to an external substrate (Slack Socket Mode, webhooks, …) and emits typed events to the host via `EmitEvent`. |

A plugin can implement any combination — tool-only, channel-only, trigger-only, or all three.

**Trust model.** Every plugin bundle is signed with a Minisign Ed25519 key. The host pins the public key on first install (Trust On First Use) and verifies every subsequent update against that pinned key. Key rotations require explicit admin approval. The admin UI gates new plugins behind a "Pending review" consent screen before any instances can be activated. This is specified in ADR-041 (plugin system architecture) and ADR-045 (signing and TOFU trust).

**SDK location.** The SDK and reference plugins live in this monorepo for v1.1 (a separate repository is planned but not yet available):

- SDK module: `plugin-sdk/` — module path `github.com/felag-engineering/gleipnir/plugin-sdk`
- Reference plugins: `plugins/ntfy/` (channel Notify only), `plugins/slack/` (all three services + OAuth)
- Minimal example: `plugin-sdk/examples/minimal-tool/`

## 2. Prerequisites

- Go 1.25 or later (matches the SDK's `go.mod`)
- The `gleipnir-plugin` CLI, built from source. The SDK ships inside the monorepo and is not yet published as a standalone module, so build it from a checkout rather than `go install ...@latest`:

```bash
cd plugin-sdk
go build -o gleipnir-plugin ./cmd/gleipnir-plugin
# or install it into $GOBIN from the checkout:
go install ./cmd/gleipnir-plugin
```

Run `gleipnir-plugin --help` to confirm. Subcommands: `new`, `validate`, `sign`, `keygen`, `run`, `package`, `gen-manifest`.

## 3. Scaffold a plugin

```bash
gleipnir-plugin new my-plugin --kind=tool
```

`--kind` accepts:

| Value | Services scaffolded |
|-------|-------------------|
| `tool` (default) | ToolService with one example tool |
| `channel` | ChannelService with Notify + Request stubs |
| `trigger` | TriggerService with one EmitEvent example |
| `combo` | All three services |

Optional flags:

```
--dir <path>           Output directory (default: ./<name>)
--module <path>        Go module path (default: example.com/<name>)
--sdk-replace <path>   Adds a go.mod replace directive to a local plugin-sdk checkout
                       (for working inside or alongside the monorepo; remove before
                       distributing the plugin)
```

After scaffolding:

```bash
cd my-plugin
go mod tidy
make build
make gen-manifest   # generates manifest.yaml from the binary
```

The scaffold generates `main.go`, `manifest.go`, `service.go`, `service_test.go`, `go.mod`, `Makefile`, `README.md`, and `.gitignore`.

**Working inside the monorepo.** All first-party plugins use `v0.0.0` as the SDK version — a workspace-only pseudo-version resolved by the repo-root `go.work`. If you are developing outside the monorepo, add a `replace` directive:

```
replace github.com/felag-engineering/gleipnir/plugin-sdk => ../path/to/plugin-sdk
```

## 4. The three services

### ToolService

Implement the `tool.Service` interface (`plugin-sdk/tool/service.go`):

```go
type Service interface {
    ListTools(ctx context.Context) ([]ToolSpec, error)
    Call(ctx context.Context, tool string, input []byte) (output []byte, err error)
}
```

`ListTools` is called once at startup and after hot-reload. Return every tool the instance currently exposes. `Call` receives the raw input JSON and returns raw output JSON. Return `pluginerr.InvalidArg(...)` for unknown tool names or malformed input; plain errors become `ERROR_CODE_INTERNAL`.

Register with `serve.WithToolHandler`:

```go
serve.Serve(
    serve.WithManifest(pluginManifest),
    serve.WithToolHandler(func(host hostv1.HostServiceClient) tool.Service {
        return NewToolService(host)
    }),
)
```

See `plugin-sdk/examples/minimal-tool/` for the canonical small example. The `echo` tool in that directory is the shortest complete implementation.

**Cancellation:** `Call` MUST return promptly when `ctx.Done()` closes. The host enforces a 5-second grace period then force-disconnects.

**Host RPC correlation:** Before every outbound host RPC (Log, EmitMetric, GetInstanceConfig, …), call `serve.WithCallContext(ctx)` to attach the host-injected call ID. Without it, the host cannot correlate the RPC back to the originating run and step.

```go
hostCtx := serve.WithCallContext(ctx)
s.host.Log(hostCtx, &hostv1.LogRequest{...})
```

### ChannelService

Implement `channel.Service` (`plugin-sdk/channel/service.go`):

```go
type Service interface {
    Notify(ctx context.Context, n Notification) error
    Request(ctx context.Context, r FeedbackRequest) error
}
```

`Notify` is fire-and-forget — a non-nil error is audited but does not fail the run. `Request` must ack synchronously (return nil) within 5 seconds, then later call the host's `WriteAuditStep` RPC with a `feedback_response` step when the human replies (see spec §4.2).

Notify-only plugins should return `pluginerr.Unimplemented("Request not supported")` from `Request`. This produces the correct application-level envelope rather than a gRPC-level error.

Register with `serve.WithChannelHandler`. See `plugins/ntfy/service.go` for the canonical Notify-only example (~100 LOC).

### TriggerService

Implement `trigger.Service` (`plugin-sdk/trigger/service.go`):

```go
type Service interface {
    Start(ctx context.Context, scope StartScope, emit func(Event) error) error
}
```

`Start` opens a long-lived connection to the external substrate (e.g. Slack Socket Mode, an SSE feed, a webhook receiver) and calls `emit` for each incoming event. It must return when `ctx.Done()` closes. If `emit(e)` returns a non-nil error, the host is unavailable — `Start` should return at that point.

The `emit` callback routes events through `HostService.EmitEvent` (spec §4.3). The host deduplicates events within a 1-hour rolling window using `Event.EventID`, so use a stable, substrate-derived ID (a ULID derived from the channel ID and timestamp works well).

`scope.WatchScope` carries the coarse instance-level subscription scope the admin configured (e.g. `{"channels": ["C012ABCDEF"]}`). Use it to filter which events you forward.

Register with `serve.WithTriggerHandler`. See `plugins/slack/service.go` for the full Socket Mode example.

## 5. The manifest

The manifest (`manifest.yaml`) is the install-time authority for everything the host needs before running the plugin: declared services, credential strategy, tool declarations, event kinds, config schemas, and the optional SBOM path.

**Author it in Go, not by hand.** Declare a `manifest.Manifest` value in `manifest.go`:

```go
var pluginManifest = manifest.Manifest{
    SchemaVersion: "v1",
    Name:          "my-plugin",
    Version:       "0.1.0",
    Description:   "Does useful things.",
    Services:      manifest.Services{Tool: "v1"},
    Auth: manifest.AuthDecl{
        Mode:     "instance_credentials",
        Strategy: manifest.AuthStrategyNone,
    },
    Tools: []manifest.ToolDecl{
        {Name: "my-tool", Description: "Does one thing."},
    },
}
```

The `manifest.Manifest` struct lives in `plugin-sdk/manifest/manifest.go`. Key fields:

| Field | Purpose |
|-------|---------|
| `SchemaVersion` | Always `"v1"` for now |
| `Name` | Lowercase, hyphens, no spaces — used in the tarball filename |
| `Version` | Plugin's own SemVer, independent of service versions |
| `Services` | Declare which services the binary implements (`Tool`, `Channel`, `Trigger`) with version string `"v1"` |
| `Auth` | Credential strategy (see §6) |
| `Tools` | List of `ToolDecl` (ToolService plugins) |
| `EventKinds` | List of `EventKindDecl` (TriggerService plugins) |
| `Channels` | List of `ChannelDecl` (ChannelService plugins) |
| `ConfigSchema` | JSON Schema (as `*yaml.Node`) for per-instance config |
| `SubscriptionSchema` | JSON Schema for the TriggerService watch scope |
| `Tier2` | Tier-2 Host RPC names the plugin requires (shown on install consent screen) |

For TriggerService plugins, use `m.MustAddEventKind` or `m.AddEventKindWithExamples` to attach a binding schema derived from a Go struct:

```go
pluginManifest.MustAddEventKind(
    "channel_message",
    "A message posted in a channel",
    &MessageFilter{},     // Go struct; fields become the operator binding form
    nil,                  // optional payload schema
)
```

**Generate `manifest.yaml`** from the binary after every change to `manifest.go`:

```bash
gleipnir-plugin gen-manifest --binary ./my-plugin --out manifest.yaml
```

Or from inside a Makefile target (as the reference plugins do):

```bash
go build -o my-plugin .
gleipnir-plugin gen-manifest --binary ./my-plugin --out manifest.yaml
```

**Verify the YAML matches the binary** (run in CI):

```bash
gleipnir-plugin validate --binary ./my-plugin --manifest manifest.yaml
```

Returns exit 0 on match, exit 1 with a diff when they diverge.

The committed `manifest.yaml` is the canonical signed artifact. Do not hand-edit it after running `gen-manifest` — the host verifies the signature against the bundle's `manifest.yaml` as-is.

**`TestManifestYAMLIsCanonical`.** Both reference plugins include this test (see `plugins/ntfy/manifest_test.go`). Copy the pattern: unmarshal the committed `manifest.yaml`, re-marshal it, and compare byte-for-byte. This catches drift when `manifest.go` is updated without regenerating the YAML.

## 6. Credentials and OAuth

Every plugin instance uses one of six auth strategies, declared in the manifest:

| Constant | `strategy` value | Description |
|----------|-----------------|-------------|
| `manifest.AuthStrategyNone` | `"none"` | No credentials required |
| `manifest.AuthStrategyStaticAPIKey` | `"static_api_key"` | Single API key stored encrypted by the host |
| `manifest.AuthStrategyHeaderSet` | `"header_set"` | One or more named HTTP headers stored encrypted |
| `manifest.AuthStrategyBasicAuth` | `"basic_auth"` | Username + password stored encrypted |
| `manifest.AuthStrategyOAuth2Authcode` | `"oauth2_authcode"` | Host runs the OAuth2 authorization code flow |
| `manifest.AuthStrategyOAuth2Clientcred` | `"oauth2_clientcred"` | Host runs the OAuth2 client credentials flow |

> **Note on `static_key` vs `static_api_key`:** The `manifest.AuthStrategyStaticAPIKey` constant has the value `"static_api_key"`. The `plugins/ntfy/manifest.go` file uses the literal string `"static_key"` (a legacy value) — that is an inconsistency in the ntfy reference plugin, not the norm. Use the `manifest.AuthStrategy*` constants for all new plugins.

The host stores and refreshes all credentials. **Plugins must never cache credentials between calls** — always fetch via `GetCredentials` at the start of each `Call`/`Notify`/`Request`. The host handles token refresh automatically for OAuth strategies.

For `oauth2_authcode` and `oauth2_clientcred`, declare defaults in the manifest under `Auth.OAuthDefaults`:

```go
Auth: manifest.AuthDecl{
    Mode:     "instance_credentials",
    Strategy: manifest.AuthStrategyOAuth2Authcode,
    OAuthDefaults: &manifest.OAuthDefaultsDecl{
        AuthorizationURL: "https://provider.example/oauth/authorize",
        TokenURL:         "https://provider.example/oauth/token",
        Scopes:           []string{"read", "write"},
    },
},
```

The host runs the OAuth dance at `/api/v1/admin/plugins/oauth/callback`. See `plugins/slack/README.md` §"Configure OAuth credentials" for the full admin UI flow.

## 7. Sign and package

### Generate a signing keypair

```bash
gleipnir-plugin keygen
# writes ~/.config/gleipnir-plugin/keys/signing.key (0600)
#         ~/.config/gleipnir-plugin/keys/signing.pub (0644)
```

Options:

```
--out-dir <dir>        Key directory (default: ~/.config/gleipnir-plugin/keys)
--name <name>          Base name for files (<name>.key, <name>.pub; default: signing)
--kdf scrypt|argon2    Passphrase KDF (default: scrypt — broadest compatibility)
--passphrase-stdin     Read passphrase from stdin (CI)
--unencrypted          Skip passphrase (testing only — emits a warning)
--force                Overwrite existing key files
```

The public key is printed to stdout on generation — save it somewhere accessible; you need it for host pinning.

**Keep the private key out of version control.** The scaffold's `.gitignore` excludes `*.key` and `*.minisig` by default.

### Sign the binary and manifest separately (optional)

If you want to sign without packaging immediately:

```bash
gleipnir-plugin sign \
  --binary ./my-plugin \
  --manifest manifest.yaml
  # writes my-plugin.minisig

# Key resolution order:
#   1. --key-stdin (CI)
#   2. env GLEIPNIR_PLUGIN_SIGNING_KEY (path or inline key content)
#   3. --key <path>
#   4. default: ~/.config/gleipnir-plugin/keys/signing.key
```

### Build the release tarball

`package` signs and bundles in one step (the canonical production path):

```bash
gleipnir-plugin package \
  --binary ./my-plugin \
  --manifest manifest.yaml \
  --out-dir dist
# writes dist/my-plugin-0.1.0.tar.gz
```

The tarball layout (per spec §14.5):

```
my-plugin-0.1.0/
  my-plugin              (binary, mode 0755)
  manifest.yaml          (mode 0644)
  my-plugin.minisig      (mode 0644)
  signing.pub            (mode 0644)
  sbom.cyclonedx.json    (mode 0644, only when --sbom is supplied)
```

Notable flags:

```
--binary <path>        Plugin binary (required)
--manifest <path>      manifest.yaml (default: manifest.yaml in cwd)
--key <path>           Secret key file (same resolution order as sign)
--key-stdin            Read key from stdin (CI)
--pubkey <path>        Public key file (default: sibling of key file)
--out-dir <dir>        Output directory (default: dist)
--sbom <path>          Optional CycloneDX SBOM JSON to bundle
--unsigned             Produce an unsigned bundle (requires
                       GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true on the host)
```

When `manifest.yaml` is absent, `package` invokes the binary with `--emit-manifest` and derives the manifest on the fly.

**Unsigned bundles.** For local development before you have a keypair, use `--unsigned` and run the host with `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true`. Every unsigned load emits a high-severity audit event and the admin UI displays a non-dismissible red banner. Signed plugins are still fully verified even in permissive mode.

**Reproducible builds.** The tarball respects `SOURCE_DATE_EPOCH` for deterministic mtimes.

## 8. Install into Gleipnir

1. **Drop the tarball** into the host's plugin directory (default: `/plugins`, configured via `GLEIPNIR_PLUGINS_DIR`). The fsnotify watcher picks it up automatically within a few seconds.

   Alternatively, upload through the admin API:
   ```
   POST /api/v1/admin/plugins  (Content-Type: application/octet-stream, max 100 MiB)
   ```

2. **Approve in the admin UI.** Navigate to `/admin/plugins`. The new plugin appears with status "Pending review". Click through the consent screen — it shows declared services, tier-2 capabilities, auth strategy, and pubkey fingerprint. Click **Approve**.

3. **Create an instance.** After approval, create one or more named instances. Each instance gets its own config, credentials, and lifecycle.

4. **Configure the instance.** Set `config` fields (via the instance settings screen) and credentials (via the Credentials screen or OAuth flow).

5. **Activate.** Instances start in `unhealthy` state. Once config and credentials are in place the instance transitions to `healthy` automatically (or on next startup).

**Key rotation.** If you update your signing key, the updated bundle will have a different public key. The host blocks the update with status `pending_key_approval` and emits an audit event. An admin must accept the new key at `/api/v1/admin/plugins/{id}/accept-new-key` before the update takes effect (ADR-045 §5.3).

**Manifest changes.** Hot-reloads that change tool declarations, event kinds, or config schema in a material way are blocked pending admin re-approval at `/api/v1/admin/plugins/{id}/accept-manifest`. Cosmetic changes (description, examples) apply automatically.

## 9. Test locally

### Unit tests with the test harnesses

`plugin-sdk/testing` provides an in-process fake host plus per-service harnesses that wire your service against the fake host over an in-memory bufconn connection. Use the harness for the most realistic unit test — your service code runs exactly as it does in production, making real gRPC calls to the fake host.

```go
import (
    "testing"
    plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
)

func TestMyTool(t *testing.T) {
    h := plugintest.NewToolHarness(t,
        NewToolService,   // your func(hostv1.HostServiceClient) tool.Service
        plugintest.WithInstanceConfigJSON(`{"server_url":"https://example.com"}`),
        plugintest.WithCredentialsJSON(`{"api_key":"tok-abc"}`),
    )
    // h.Client is a live gRPC ToolServiceClient connected to your service.
    // h.Host is the FakeHost — inspect it after each call.

    out, err := h.Call(ctx, "my-tool", []byte(`{"message":"hello"}`))
    // ...

    metrics := h.Host.Metrics()
    logs    := h.Host.Logs()
    events  := h.Host.Events()
}
```

Equivalent harnesses exist for all three services:

| Constructor | Type returned | Client field type |
|-------------|--------------|-------------------|
| `NewToolHarness(t, factory, opts...)` | `*ToolHarness` | `toolv1.ToolServiceClient` |
| `NewChannelHarness(t, factory, opts...)` | `*ChannelHarness` | `channelv1.ChannelServiceClient` |
| `NewTriggerHarness(t, factory, opts...)` | `*TriggerHarness` | `triggerv1.TriggerServiceClient` |

Each harness registers a `t.Cleanup` that tears down both the service and host servers, so tests need no explicit teardown.

If you need the fake host alone (e.g. to drive your service constructor directly without a gRPC round-trip), use `NewFakeHost` with `Register`:

```go
host := plugintest.NewFakeHost(
    plugintest.WithInstanceConfigJSON(`{}`),
)
```

Available options for `NewFakeHost` / harnesses:

| Option | Purpose |
|--------|---------|
| `WithInstanceConfigJSON(s)` | Sets the JSON returned by `GetInstanceConfig` |
| `WithCredentialsJSON(s)` | Sets the JSON returned by `GetCredentials` |
| `WithRunContext(rc)` | Sets the run context returned by `GetRunContext` |
| `WithLogger(l)` | Receives forwarded `Log` RPCs |
| `OnEmitEvent(cb)` | Callback invoked synchronously for each `EmitEvent` call |
| `WithRunHistory(runs)` | Seeds the Tier-2 `RunHistoryRead` stub |
| `WithUserDirectory(users)` | Seeds the Tier-2 `UserDirectoryRead` stub |

Inspection methods on `FakeHost`: `Events()`, `Metrics()`, `AuditSteps()`, `Logs()`, `Health()`. Call `Reset()` between sub-tests to clear recorded calls.

**Import alias.** Import `plugin-sdk/testing` as `plugintest` (not `testing`) to avoid shadowing the stdlib package.

Both reference plugins use this pattern extensively. See `plugins/ntfy/service_test.go` and `plugins/slack/service_test.go` for complete examples.

### Interactive dev with `gleipnir-plugin run`

`run` boots the plugin binary against an in-process fake host without a live Gleipnir instance. Three mutually exclusive modes:

```bash
# Scripted batch mode — run a YAML scenario and print pass/fail
gleipnir-plugin run ./my-plugin --scenario tests/smoke.yaml

# Capture events emitted by TriggerService to a JSONL file
gleipnir-plugin run ./my-plugin --capture events.jsonl --watch-scope '{}'

# Replay a captured event file back into the plugin
gleipnir-plugin run ./my-plugin --replay events.jsonl
```

Additional flags for `run`:

```
--max-events N         Stop capture/replay after N events (0 = unlimited)
--filter key=value     Filter events by field during replay
--continue-on-error    Continue replay after a plugin non-zero exit
--timeout <duration>   Timeout for the entire operation (default: 30s)
```

`run` does not simulate signature verification, version negotiation, or a live LLM/SQLite. It is a development convenience, not a pre-release gate — use `gleipnir-plugin validate` for that.

## 10. Reference links

| Resource | Path |
|----------|------|
| Plugin system spec | [`docs/developer/plugin-system-spec.md`](plugin-system-spec.md) |
| ADR tracker | [`docs/developer/ADR_Tracker.md`](ADR_Tracker.md) |
| SDK README | [`plugin-sdk/README.md`](../../plugin-sdk/README.md) |
| Minimal tool example | [`plugin-sdk/examples/minimal-tool/`](../../plugin-sdk/examples/minimal-tool/) |
| ntfy reference (channel) | [`plugins/ntfy/`](../../plugins/ntfy/) |
| slack reference (full) | [`plugins/slack/`](../../plugins/slack/) |

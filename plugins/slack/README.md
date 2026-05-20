# slack

**Status: All three services implemented (#233, #234, #235).** OAuth authcode integration documented in #236.

Remaining issues:

- [#237](https://github.com/felag-engineering/gleipnir/issues/237) — End-to-end test, packaging, signing

Parent tracking issue: [#162](https://github.com/felag-engineering/gleipnir/issues/162)

## What this plugin declares

- **ToolService v1** — `post_message`, `list_channels`, `search_messages`, `react`, `set_topic` (implemented by #233)
- **TriggerService v1** — `channel_message` event kind via Slack Socket Mode (#234). Note: subscription-scope channel filtering matches by **channel ID** only (e.g. `C012ABCDEF`); name-based filtering (e.g. `#incidents`) silently does not match because Socket Mode events carry only IDs. Resolving names is future work.
- **ChannelService v1** — `Notify` and `Request` via Slack DMs and posts (#235)
- **Auth strategy** — `oauth2_authcode` with Slack's authorization and token endpoints

## Layout

```
slack/
  main.go            — calls serve.Serve() with all three service factories
  service.go         — ToolService, TriggerService, ChannelService implementations
  tools.go           — typed tool params + handlers (#233)
  translator.go      — pure functions translating Socket Mode events to channel_message payloads (#234)
  scope.go           — SlackSubscriptionScope decode + matches helpers (#234)
  manifest.go        — pluginManifest variable (source of truth for metadata + event kinds)
  manifest.yaml      — canonical YAML projection of manifest.go (generated)
  manifest_test.go   — TestManifestYAMLIsCanonical: round-trip + Go-source equality
  service_test.go    — service-level behavior assertions (tool calls, trigger lifecycle)
  translator_test.go — pure translator unit tests (#234)
  scope_test.go      — subscription-scope matching unit tests (#234)
  Makefile           — build / test / manifest / validate / clean targets
  .gitignore         — excludes the compiled binary and signing artifacts
  go.mod             — per-plugin module; resolved by the repo-root go.work
```

## Channel (per-audience) config schema

| Field              | Required | Description |
|--------------------|----------|-------------|
| `channel`          | yes      | Slack channel or user DM target (e.g. `#general` or `@username`) |
| `mention`          | no       | Slack user or group to mention in the message (e.g. `@oncall`) |
| `response_buttons` | no       | Custom button options for `Request` messages (defaults to Approve/Reject) |

## Auth

This plugin uses `oauth2_authcode`. The host runs the OAuth 2.0 authorization
code flow on behalf of the plugin and stores the resulting access token in
encrypted credentials. A `client_id` and `client_secret` must be configured
per-instance by an admin — there are no baked-in defaults (Option A per #236;
see §"Configure OAuth credentials" below).

Default OAuth endpoints declared in the manifest:
- Authorization URL: `https://slack.com/oauth/v2/authorize`
- Token URL: `https://slack.com/api/oauth.v2.access`

### Configure OAuth credentials

#### Step 1 — Create your Slack app

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and click **Create New App**.
2. Choose **From a manifest** to use the one-click YAML below (see
   §"Slack app manifest (one-click)"), or choose **From scratch** and configure
   scopes manually.
3. Select your workspace and confirm.

#### Step 2 — Add the Gleipnir redirect URL

1. In your new Slack app's settings, go to **OAuth & Permissions**.
2. Under **Redirect URLs**, click **Add New Redirect URL** and enter:
   ```
   https://<your-gleipnir-public-url>/api/v1/admin/plugins/oauth/callback
   ```
   Replace `<your-gleipnir-public-url>` with the value you configured in
   Gleipnir Admin → System → Public URL.
3. Click **Save URLs**.

#### Step 3 — Copy credentials from Slack

1. In your Slack app's settings, go to **Basic Information**.
2. Under **App Credentials**, copy the **Client ID** and **Client Secret**.

#### Step 4 — Paste credentials into Gleipnir

1. Open Gleipnir Admin → Plugins → Slack → your plugin instance.
2. In the **Credentials** section, paste the **Client ID** and **Client Secret**.
3. Save.

#### Step 5 — Authorize

1. Click **Authorize with Slack** in the Gleipnir admin UI.
2. Your browser redirects to Slack's OAuth consent screen.
3. Click **Allow**.
4. Slack redirects back to Gleipnir's callback URL.
5. The admin UI reloads with `?oauth_ok=1` and the plugin instance transitions
   to **healthy**.
6. The first tool call runs `auth.test` as a final verification gate.

### Slack app manifest (one-click)

Paste this YAML into Slack's "Create from manifest" flow. Replace
`<your-public-url>` with your Gleipnir instance's public URL.

```yaml
display_information:
  name: Gleipnir
  description: Gleipnir homelab agent orchestrator
features:
  bot_user:
    display_name: Gleipnir
    always_online: false
  socket_mode_enabled: true
oauth_config:
  redirect_urls:
    - https://<your-public-url>/api/v1/admin/plugins/oauth/callback
  scopes:
    bot:
      - channels:history
      - channels:read
      - chat:write
      - groups:history
      - im:history
      - im:write
      - mpim:history
      - search:read
      - users:read
settings:
  event_subscriptions:
    bot_events:
      - message.channels
      - message.groups
      - message.im
      - message.mpim
      - app_mention
  interactivity:
    is_enabled: true
  org_deploy_enabled: false
  socket_mode_enabled: true
  token_rotation_enabled: false
```

### App-level token (xapp-)

TriggerService uses Slack's Socket Mode transport, which requires an additional
**app-level token** (prefix `xapp-`) separate from the OAuth bot token. Socket
Mode is homelab-friendly because it does not require a public webhook endpoint.

To generate the token:

1. Open your Slack app settings at [api.slack.com/apps](https://api.slack.com/apps).
2. Select your app → **Settings → Socket Mode**.
3. Enable Socket Mode if not already enabled.
4. Under **App-Level Tokens**, click **Generate Token and Scopes**.
5. Give the token a name (e.g. `gleipnir-socket-mode`) and add the scope `connections:write`.
6. Click **Generate** and copy the token (it starts with `xapp-`).
7. In the Gleipnir admin UI, navigate to the Slack plugin instance config and
   paste the token into the `app_level_token` field.

> **Note:** The `app_level_token` field is stored as plain text in instance
> config in this release. A follow-up issue will add `gleipnir-secret` format
> annotation so the admin GET endpoint redacts the value to `"***"` (mirroring
> the ADR-039 pattern for auth headers).

## Build

```sh
make build
# or
go build -o slack .
```

## Test

From this directory:

```sh
make test
# or
go test ./...
```

Or for just this plugin from the repo root (workspace mode resolves the prefix to the registered module):

```sh
go test ./plugins/slack/...
```

The broader `go test ./plugins/...` does not work, because each first-party plugin is its own module and the gleipnir root module owns nothing under `./plugins/`. CI iterates the directories explicitly — see the `Test plugins (workspace)` step in `.github/workflows/ci.yml`.

| Test | What it asserts |
|------|-----------------|
| `TestManifestYAMLIsCanonical` | `manifest.yaml` round-trips and matches `manifest.go` |
| `TestTriggerStartFailedPreconditionWithoutToken` | Missing `app_level_token` → `FailedPrecondition` + UNHEALTHY/config_missing |
| `TestTriggerStartEmitsEventOnFakeSocketModeMessage` | Fake runner delivers event → `EmitEvent` called with correct kind/id/payload, Ack called once |
| `TestTriggerStartHonorsSubscriptionScopeChannels` | Excluded channel → zero `EmitEvent`, Ack still called |
| `TestTriggerStartHealthUnhealthyOnInvalidAuth` | Runner returns `invalid_auth` → `Unauthenticated` + UNHEALTHY/auth_expired |
| `TestTranslate` | Table-driven: MessageEvent, AppMentionEvent, subtypes, nil data, malformed ts |
| `TestDeriveEventIDIsDeterministic` | Same (channelID, ts) → same ULID across two calls |
| `TestDeriveEventIDDiffersForDifferentInputs` | Different inputs → different ULIDs |
| `TestParseSlackTS` | Valid ts parses correctly; malformed ts falls back to now |
| `TestSlackSubscriptionScopeMatches` | Table-driven scope matching: by-id, by-name, mention-only, composed |
| `TestDecodeSubscriptionScope` | Empty/`{}` → zero value; malformed → error |
| `TestToolListToolsAdvertisesAll` | `ListTools` returns exactly 5 tools with valid JSON-Schema InputSchema |
| `TestToolCancelIsNoOp` | `Cancel` returns an empty response with no error |
| `TestToolCalls` | Table-driven: happy path × 5 tools, error cases, auth failures, unknown tool |
| `TestToolCallMetricOutcomeLabel` | Metric `outcome` label is `"ok"` on success and `"error"` on error |
| `TestToolCallCtxCancel` | Cancelled context surfaces as `UNAVAILABLE` |
| `TestMapErr` | Slack error-code strings map to the correct `ErrorCode` + health hint |
| `TestToolService_Call_AuthTestFails_SetsUnhealthy` | auth.test → invalid_auth → PERMISSION + UNHEALTHY/auth_expired, tool not called |
| `TestToolService_Call_AuthTestOnce_SkippedOnSubsequentCalls` | Same token → auth.test called once; verifiedToken short-circuit skips second |
| `TestToolService_Call_AuthTestReruns_OnTokenRotation` | Token changes between calls → auth.test called twice (one per distinct token) |
| `TestChannelNotifyWithMention` | Non-empty cfg.Mention → posted text is prefixed with `<mention> <body>` |
| `TestChannelNotifyMissingCredentials` | Empty credentials JSON → ok=false, ErrorCode PERMISSION, SetHealthState called with detail=auth_missing |
| `TestChannelService_Notify_DoesNotPerformAuthTest` | ChannelService.Notify never calls auth.test (direct contract; counter stays at 0 after two Notify calls) |
| `TestChannelService_Request_HandleInteractiveTakesCorrelation` | handleInteractive consumes the correlation entry via take(); after WriteAuditStep fires, take() returns found=false |

## Manifest

`manifest.go` is the source of truth. `manifest.yaml` is its canonical
projection — generated by `gleipnir-plugin gen-manifest`, never written by hand.

If you edit `manifest.go`, regenerate the YAML:

```sh
make manifest
```

Then run `go test ./...` to confirm `TestManifestYAMLIsCanonical` still passes.

## Install and audience setup flow

1. **Build** the binary:
   ```sh
   make build
   ```

2. **Generate signing keys** (not committed — keep the private key secret):
   ```sh
   gleipnir-plugin keygen --out slack.key
   # produces slack.key (private) and slack.pub (public)
   ```

3. **Sign and package:**
   ```sh
   gleipnir-plugin sign    --binary ./slack --manifest manifest.yaml --key slack.key
   gleipnir-plugin package --binary ./slack --manifest manifest.yaml --sig slack.sig --out slack.tar.gz
   ```

4. **Install** via the Gleipnir admin UI at `/admin/plugins` → "Install plugin"
   → upload `slack.tar.gz`.

5. **Configure OAuth credentials** — follow the steps in §"Configure OAuth credentials" above.

6. **Add an audience entry** to a policy:
   - Select the Slack plugin as the channel.
   - Set `channel` in the channel config (required).
   - Optionally set `mention` for an at-mention in the message.
   - Toggle `notify: true` and/or `request: true`.

## Using outside the monorepo

This plugin's `go.mod` declares:

```
require github.com/felag-engineering/gleipnir/plugin-sdk v0.0.0
```

`v0.0.0` is a workspace-only pseudo-version resolved by the repo-root `go.work`.
Outside the monorepo, add a `replace` directive:

```
replace github.com/felag-engineering/gleipnir/plugin-sdk => ../path/to/plugin-sdk
```

## Tools

The Slack plugin exposes five tools through the `ToolService`. At runtime the host
prefixes each name with the instance name, so they appear as
`<instance>.post_message`, `<instance>.list_channels`, etc. (e.g.
`slack-prod.post_message`).

### `post_message`

Post a plain-text message to a Slack channel or DM.

**Input:**
```json
{
  "channel": "C1234567890",
  "text": "Hello from Gleipnir!",
  "thread_ts": "1700000000.123456"
}
```

`thread_ts` is optional — omit it to post to the channel root.

**Output:**
```json
{"channel": "C1234567890", "ts": "1700000001.000000"}
```

### `list_channels`

List Slack channels visible to the bot user.

**Input:**
```json
{
  "exclude_archived": true,
  "limit": 200,
  "types": "public_channel,private_channel"
}
```

All fields are optional. `limit` defaults to 200 (max 1000). `types` defaults
to `public_channel`.

**Output:**
```json
{
  "channels": [
    {"id": "C001", "name": "general", "is_private": false, "is_archived": false, "num_members": 42}
  ],
  "next_cursor": ""
}
```

### `search_messages`

Search messages across the workspace using Slack's full-text search
(`search.messages` API).

**Input:**
```json
{"query": "deployment failed", "count": 20}
```

`count` is optional (1–100, defaults to 20).

**Output:**
```json
{
  "matches": [
    {
      "channel": "C001",
      "user": "U001",
      "text": "deployment failed at 03:00 UTC",
      "ts": "1700000001.000000",
      "permalink": "https://myworkspace.slack.com/archives/C001/p1700000001000000"
    }
  ],
  "total": 1
}
```

### `react`

Add an emoji reaction to a Slack message.

**Input:**
```json
{
  "channel": "C1234567890",
  "timestamp": "1700000001.000000",
  "name": "thumbsup"
}
```

`name` is the reaction emoji name without colons.

**Output:**
```json
{"ok": true}
```

### `set_topic`

Set the topic of a Slack channel.

**Input:**
```json
{"channel": "C1234567890", "topic": "Deployment in progress — do not merge"}
```

**Output:**
```json
{"ok": true, "topic": "Deployment in progress — do not merge"}
```

## Required Slack scopes

The following OAuth 2.0 bot token scopes must be granted when installing the
Slack app. These are declared in `manifest.go`'s `OAuthDefaults.Scopes` list
and shown in the admin UI during install.

| Scope             | Used by |
|-------------------|---------|
| `channels:history` | **Reserved; no current tool uses it** |
| `channels:read`   | `list_channels` (public channels) |
| `chat:write`      | `post_message` |
| `groups:history`  | **Reserved; no current tool uses it** |
| `im:history`      | **Reserved; no current tool uses it** |
| `im:write`        | `post_message` to DMs |
| `mpim:history`    | **Reserved; no current tool uses it** |
| `search:read`     | `search_messages` |
| `users:read`      | user lookup in various responses |

> **Note on `*:history` scopes:** The four `*:history` scopes (`channels:history`,
> `groups:history`, `im:history`, `mpim:history`) are reserved for future
> history-reading tools (e.g., a `read_recent_messages` tool that calls
> `conversations.history`). The current plugin code does **not** call these
> endpoints at runtime. Socket Mode event delivery (TriggerService) uses the
> app-level `xapp-` token and does **not** require these bot-token scopes.
> Operators who decline these scopes at OAuth time will still get a working
> installation for the currently-implemented tools (`post_message`,
> `list_channels`, `search_messages`, `react`, `set_topic`) and the
> `channel_message` trigger; they are requested up-front per the issue AC so the
> same OAuth grant covers planned tools without a re-authorize round trip.

**Note on `search:read`:** This scope is available for bot tokens but may not
be granted during OAuth if the Slack app was configured before `search:read`
was added. Bot-only installs that are missing this scope will see a runtime
`PERMISSION` error (`missing_scope`) for `search_messages` calls, and the
plugin will be set to `UNHEALTHY` with detail `auth_expired` in the operator
UI. To resolve: grant `search:read` in the Slack app settings and re-authorize
the plugin instance.

## Token refresh

Slack v2 bot tokens (`xoxb-` prefix) do not expire under normal circumstances
— they remain valid indefinitely unless explicitly revoked by a workspace admin
or the app is uninstalled. As a result, the Phase-7 credential refresh scanner
has nothing to refresh for Slack in the steady state. The `auth_expired` health
transition fires only when a token is revoked: the next `ToolService.Call`
invocation runs `auth.test`, receives `invalid_auth` or `token_revoked`, and
sets the plugin to `UNHEALTHY`. The operator then re-authorizes via the admin UI
to restore the healthy state.

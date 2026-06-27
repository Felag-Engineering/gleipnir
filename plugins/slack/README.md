# slack

The kitchen-sink reference plugin: it exercises all three plugin service
surfaces (tools, triggers, channels) plus the host-driven `oauth2_authcode`
credential strategy. If you want to see how a full-featured Gleipnir plugin is
put together, read this one alongside `plugins/ntfy` (the minimal channel-only
reference).

## What this plugin declares

- **ToolService v1** — `post_message` (with Block Kit), `list_channels`, `react`, `set_topic`, `read_thread`, `read_history`, `update_message`, `delete_message`, `lookup_user` (implemented by #233, #626)
- **TriggerService v1** — five event kinds via Slack Socket Mode (#234, #621, #623):
  - `channel_message` — any human message in a watched channel; `mention_only: true` binding selects mentions. Use `channel_id` (exact channel ID, e.g. `C012ABCDEF`) in the binding to restrict to a specific channel — the searchable channel picker in the policy editor shows available IDs. Name-based filtering is not supported because Socket Mode events carry only IDs.
  - `direct_message` — a 1:1 DM sent directly to the bot. Enable via `direct_messages: true` in the instance subscription scope.
  - `slash_command` — a workspace slash command (e.g. `/gleipnir deploy staging`). Enable via `slash_commands: true` in the instance subscription scope.
  - `message_shortcut` — a message shortcut invoked on a specific message (carries message text, ts, channel). Enable via `shortcuts: true` in the instance subscription scope.
  - `global_shortcut` — a global shortcut invoked from anywhere (no message context). Enable via `shortcuts: true` in the instance subscription scope.
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
  sockethub.go       — Socket Mode connection lifecycle (xapp- token, reconnect)
  hub_registry.go    — per-instance Socket Mode hub sharing across services
  channel_blocks.go  — Block Kit message builders for Request buttons (#235)
  channel_state.go   — in-flight Request correlation map (callback routing)
  manifest.go        — pluginManifest variable (source of truth for metadata + event kinds)
  manifest.yaml      — canonical YAML projection of manifest.go (generated)
  manifest_test.go   — TestManifestYAMLIsCanonical: round-trip + Go-source equality
  service_test.go    — service-level behavior assertions (tool calls, trigger/channel lifecycle)
  tools_test.go      — per-tool handler tests + rate-limit behavior (#233)
  translator_test.go — pure translator unit tests (#234)
  scope_test.go      — subscription-scope matching unit tests (#234)
  channel_blocks_test.go / channel_state_test.go / hub_registry_test.go — channel/hub unit tests
  Makefile           — build / test / manifest / validate / clean targets
  .gitignore         — excludes the compiled binary and signing artifacts
  go.mod             — per-plugin module; resolved by the repo-root go.work
```

## Subscription scope and the host trigger stream

The Gleipnir host only opens the `TriggerService.Start` stream when the instance
`subscription_scope_json` is non-empty (i.e. not `""` or `"{}"`). A fresh
instance defaults to `"{}"`, so the stream never opens unless the operator
configures a non-empty scope.

For `channel_message` and `direct_message` this is natural — the `channels`
list or `direct_messages: true` flag makes the JSON non-empty. For slash
commands and shortcuts (which are not channel-scoped), use the explicit flags:

| Scope flag          | When to set |
|---------------------|-------------|
| `slash_commands: true` | Instance is used **only** for slash commands and has no channel watch |
| `shortcuts: true`   | Instance is used **only** for shortcuts and has no channel watch |

An instance already watching channels (or with `direct_messages: true`) also
receives slash and shortcut events without setting these flags — the host gate
only checks that the JSON is non-empty.

These flags are stream-open enablers only. Plugin-side delivery of slash/shortcut
events bypasses the channel scope filter (`scope.matches`) — slash commands and
shortcuts are explicit user intent from any surface.

## Trigger binding (`channel_message`)

Policies that use the `channel_message` trigger can filter events using the
following binding fields (all optional; fields are ANDed together):

| Field          | Match  | Notes |
|----------------|--------|-------|
| `channel_id`   | equals | Exact Slack channel ID (e.g. `C012ABCDEF`). Pick from the searchable channel list in the policy editor. Matches the message's stable channel ID — never the display name. |
| `channel_type` | equals | Slack channel kind: `channel` (public), `group` (private), `mpim` (multi-party DM). Empty matches any kind. Note: 1:1 DMs (`im`) now route to `direct_message`, not `channel_message`. |
| `text`         | contains | Substring anywhere in the message text |
| `mention_only` | flag   | Only fire when the bot is @-mentioned (detected via text-scan for both `message` and `app_mention` events). Never fires for DMs. |
| `user`         | equals | Exact Slack user ID (e.g. `U012AB3CD`). Pick from the searchable user list in the policy editor. |

See [§"Triggering runs from Slack messages"](#triggering-runs-from-slack-messages)
for worked examples and routing behaviour.

## Trigger binding (`direct_message`)

Policies that use the `direct_message` trigger can filter events using:

| Field  | Match    | Notes |
|--------|----------|-------|
| `text` | contains | Substring anywhere in the DM text |
| `user` | equals   | Exact Slack user ID of the sender. Pick from the searchable user list in the policy editor. |

`channel`, `channel_type`, and `mention_only` are absent: they have no meaning
in a 1:1 DM conversation.

## Trigger binding (`slash_command`)

Policies that use the `slash_command` trigger can filter events using:

| Field     | Match    | Notes |
|-----------|----------|-------|
| `command` | equals   | Exact slash command name, e.g. `/gleipnir`. Leave empty to match any command. |
| `text`    | contains | Substring anywhere in the command arguments |

Example — route `/gleipnir deploy …` to a deployment policy:

```yaml
name: slack-deploy-command
trigger:
  type: subscribed
  source: slack-prod        # your Slack plugin instance name
  event_kind: slash_command
  binding:
    command: "/gleipnir"
    text: "deploy"
capabilities:
  tools:
    - tool: slack-prod.post_message
agent:
  task: |
    The trigger payload is a slash command. The "text" field contains the
    arguments after /gleipnir. Execute the requested deployment and reply
    using post_message (response_url is in the payload for direct replies).
```

**Slack app config:** register the slash command in your Slack app settings
(api.slack.com → your app → Slash Commands → Create New Command). Set the
command name (e.g. `/gleipnir`); the Request URL can be left blank when Socket
Mode is enabled — Slack delivers slash commands over the existing Socket Mode
connection. No extra OAuth scopes are required.

**Ack semantics:** the hub acks within Slack's ~3s window immediately before
dispatching. The agent run executes asynchronously; use `post_message` or the
`response_url` from the payload to post results back to the channel.

## Trigger binding (`message_shortcut`)

Policies that use the `message_shortcut` trigger can filter events using:

| Field         | Match  | Notes |
|---------------|--------|-------|
| `callback_id` | equals | Shortcut callback_id as registered in Slack (e.g. `run_agent_on_message`). Leave empty to match any shortcut. |

Example — route a "Run agent on this message" shortcut to a summarizer policy:

```yaml
name: slack-message-shortcut
trigger:
  type: subscribed
  source: slack-prod
  event_kind: message_shortcut
  binding:
    callback_id: "run_agent_on_message"
capabilities:
  tools:
    - tool: slack-prod.post_message
agent:
  task: |
    The trigger payload is a message shortcut. The "text" field contains the
    target message text; "ts" and "channel_id" identify it. Summarize the
    message and reply via post_message.
```

## Trigger binding (`global_shortcut`)

Policies that use the `global_shortcut` trigger can filter events using:

| Field         | Match  | Notes |
|---------------|--------|-------|
| `callback_id` | equals | Shortcut callback_id as registered in Slack. Leave empty to match any global shortcut. |

**Slack app config (shortcuts):** register shortcuts in your Slack app settings
(api.slack.com → your app → Interactivity & Shortcuts → Create New Shortcut).
For message shortcuts, choose "On messages"; for global shortcuts, choose "Global".
Set a unique callback_id (e.g. `run_agent_on_message`). Interactivity must be
enabled. Both shortcut types are delivered over the existing Socket Mode
connection — no Request URL is needed. No extra OAuth scopes are required.

**Prerequisites** for `direct_message` events to arrive:

1. Enable `direct_messages: true` in the instance subscription scope (Admin → Plugins → your Slack instance → Subscription Scope).
2. The Slack app must subscribe to the `message.im` bot event (already in the one-click app manifest below).
3. **App Home → Messages Tab → "Allow users to send Slash commands and messages"** must be enabled in your Slack app settings (api.slack.com/apps → your app → App Home → Show Tabs → Messages Tab). This is a Slack product setting outside Gleipnir's control.

The `im:history` OAuth scope is already included in `oauth_defaults` — no re-authorization is needed.

**Self-trigger guard:** the bot's own messages (in channels or DMs) are never emitted as trigger events. This is enforced by checking `user == botUserID` (fetched via `auth.test` at stream open). The bot user ID is cached per stream; if `auth.test` fails at startup, the guard is inert (no worse than pre-#621 behavior).

## Triggering runs from Slack messages

### How a policy binds

A policy fires on `channel_message` events by declaring a `subscribed` trigger
in its YAML. The policy form editor (Admin → Policies → New Policy) is the real
authoring surface for this — per ADR-019/ADR-048 the word "subscribed" is never
shown in the UI, and the form produces this YAML as the stored/API payload:

```yaml
name: recipe-finder
trigger:
  type: subscribed
  source: slack-prod          # the plugin INSTANCE NAME (not a ULID)
  event_kind: channel_message
  binding:                    # optional; all fields ANDed; omit to match every message
    text: "recipe:"
capabilities:
  tools:
    - tool: slack-prod.post_message
agent:
  task: |
    The trigger payload is a Slack channel_message. Treat everything after
    "Recipe:" as a recipe request and reply in-thread with suggestions.
```

`source` must be the plugin **instance name** (e.g. `slack-prod`), not the
plugin ID. Binding fields are ANDed — a message must satisfy every field that is
set. For the full list of binding fields and their match semantics, see the table
in [§"Trigger binding (`channel_message`)"](#trigger-binding-channel_message).

### Routing is fan-out (no single winner)

The Gleipnir dispatcher evaluates **every** active subscribed policy against the
incoming event. Any policy whose `source`, `event_kind`, and `binding` all match
fires its own independent run. There is no "route to exactly one policy", no
default/else policy, and no arbitration across policies.

Two policies with overlapping bindings will both fire. Design bindings with
distinct `text` substrings or `channel_id` filters so messages reach exactly the
policy you intend:

- `text: "recipe:"` — fires on any message containing "recipe:"
- `text: "deploy:"` — a distinct non-overlapping substring

Note: this no-arbitration statement is cross-policy. A single policy's own
concurrency setting (`concurrency: skip` / `queue` / `parallel`) still governs
how many runs that policy starts for its own repeated matches.

### DM troubleshooting

With the `direct_message` event kind (#621), DMs now have a dedicated routing
surface. If a DM produces no run, check the following:

**`direct_messages` flag in subscription scope**

The instance subscription scope (Admin → Plugins → your Slack instance →
Subscription Scope) must have `direct_messages: true`. Without it, DM events
are suppressed before any policy is evaluated. The `channels` allow-list does
not affect DMs — the `direct_messages` flag is the sole gate.

**Slack-side prerequisites**

These are external Slack settings, not Gleipnir config:

1. **`message.im` bot event** — the Slack app must subscribe to the `message.im`
   event so Slack delivers DMs to the bot at all. The one-click app manifest in
   [§"Slack app manifest (one-click)"](#slack-app-manifest-one-click) already
   includes this event.

2. **App Home → Messages Tab → "Allow users to send Slash commands and messages"**
   — this toggle must be enabled in your Slack app's settings (api.slack.com/apps
   → your app → App Home → Show Tabs → Messages Tab). Without it, users cannot
   send DMs to the bot even if `message.im` is subscribed. This is a Slack product
   setting with no equivalent in Gleipnir's configuration.

### Worked examples

#### Example 1 — Keyword routing with channel filter

Fires when a message in a specific channel contains the word "recipe:".

```yaml
name: recipe-finder
trigger:
  type: subscribed
  source: slack-prod          # your Slack plugin instance name
  event_kind: channel_message
  binding:
    channel_id: "C012ABCDEF"  # pick from the searchable channel list in the policy editor
    text: "recipe:"           # case-sensitive substring anywhere in the message
capabilities:
  tools:
    - tool: slack-prod.post_message
agent:
  task: |
    The trigger payload is a Slack channel_message. Treat everything after
    "recipe:" as a recipe request and reply in-thread with suggestions.
```

Matching Slack message: `recipe: something with chickpeas`

Because routing is fan-out, combine `channel_id` and/or `text` filters to
keep policies from overlapping.

#### Example 2 — DM-only bot

Fires on any direct message to the bot using the `direct_message` event kind.
Enable `direct_messages: true` in the instance subscription scope first.

```yaml
name: slack-dm-assistant
trigger:
  type: subscribed
  source: slack-prod
  event_kind: direct_message   # dedicated DM surface; 1:1 DMs only
capabilities:
  tools:
    - tool: slack-prod.post_message
agent:
  task: |
    The trigger payload is a direct message to the bot. Help the user and
    reply in the same DM.
```

Matching Slack message: any DM, e.g. `what's on my calendar today?`

Prerequisites:
- Set `direct_messages: true` in the instance subscription scope (Admin →
  Plugins → your Slack instance → Subscription Scope).
- Slack app must subscribe to `message.im` (already in the one-click manifest)
  and the **App Home → Messages Tab → "Allow users to send Slash commands and
  messages"** toggle must be enabled in your Slack app settings (external Slack
  setting).

Note: policies that previously used `channel_message` + `channel_type: im`
to route DMs should migrate to `direct_message`. The old approach required an
empty `channels` scope and was silently broken when `mention_only` was set.

#### Example 3 — Mention-in-channel

Fires only when the bot is @-mentioned in a public or private channel. DMs are
excluded by construction — a DM is never a mention.

```yaml
name: oncall-mention-handler
trigger:
  type: subscribed
  source: slack-prod
  event_kind: channel_message
  binding:
    mention_only: true      # only when bot is @-mentioned (channel events only — never DMs)
capabilities:
  tools:
    - tool: slack-prod.post_message
agent:
  task: |
    The trigger payload is a channel message that @-mentioned the bot.
    Answer the mention in-thread.
```

Matching Slack message (public channel): `@Gleipnir summarize the last deploy?`

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

After creating the app from this manifest, register slash commands and shortcuts
separately in the Slack app settings (they cannot be declared in the app manifest
YAML in the same step):
- **Slash Commands** → Create New Command: set the command name (e.g. `/gleipnir`). The Request URL is not required for Socket Mode apps.
- **Interactivity & Shortcuts** → Create New Shortcut: add a message shortcut and/or global shortcut with the callback_id values used in your policy bindings.

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

> **Security note:** The `app_level_token` field is marked `x-gleipnir-secret: true`
> in the manifest. The admin GET endpoint redacts it to `"***"` — the raw token is
> never returned after it is written. Use the per-field PUT endpoint
> (`PUT /api/v1/admin/plugins/{id}/instances/{iid}/config/app_level_token`) to
> update the token without transmitting all other config values (mirrors the
> ADR-039 pattern for MCP server auth headers).

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

A representative slice of the suite (the channel `Request` interactive-callback,
hub-registry, and correlation-map tests are omitted here for brevity — run
`go test -v ./...` for the full list):

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
| `TestSlackSubscriptionScopeMatches` | Table-driven scope matching: by channel ID (ID-only, per `scope.go`), mention-only, composed |
| `TestDecodeSubscriptionScope` | Empty/`{}` → zero value; malformed → error |
| `TestToolListToolsAdvertisesAll` | `ListTools` returns exactly the four tools with valid JSON-Schema InputSchema |
| `TestToolCancelIsNoOp` | `Cancel` returns an empty response with no error |
| `TestToolCalls` | Table-driven: happy path × 4 tools, error cases, auth failures, unknown tool |
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
   gleipnir-plugin keygen --out-dir . --name slack --unencrypted
   # produces slack.key (private) and slack.pub (public)
   ```

3. **Sign and package** (the `package` subcommand signs internally; a separate
   `sign` step is not required):
   ```sh
   gleipnir-plugin package --binary ./slack --manifest manifest.yaml \
     --key slack.key --pubkey slack.pub --out-dir dist
   # produces dist/slack-0.1.1.tar.gz
   ```

4. **Install** via the Gleipnir admin UI at `/admin/plugins` → "Install plugin"
   → upload `dist/slack-0.1.1.tar.gz`.

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

The Slack plugin exposes tools through the `ToolService`. At runtime the host
prefixes each name with the instance name, so they appear as
`<instance>.post_message`, `<instance>.list_channels`, etc. (e.g.
`slack-prod.post_message`).

Each tool is independently grantable — a policy grants only the names it needs
(per ADR-001 hard capability enforcement).

### `post_message`

Post a message to a Slack channel or DM. Supports optional Block Kit blocks for
rich formatting alongside plain text.

**Input:**
```json
{
  "channel": "C1234567890",
  "text": "Hello from Gleipnir!",
  "thread_ts": "1700000000.123456"
}
```

`thread_ts` is optional — omit it to post to the channel root. `text` and `blocks`
are both optional, but at least one must be provided. When `blocks` is set, `text`
is used as the notification fallback (shown in push notifications and clients that
don't render Block Kit).

**Block Kit example:**
```json
{
  "channel": "C1234567890",
  "text": "Status update",
  "blocks": [
    {"type": "section", "text": {"type": "mrkdwn", "text": "*Deployment complete* :white_check_mark:"}}
  ]
}
```

`blocks` must be a JSON **array**, not an object.

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

### `read_thread`

Read messages in a Slack thread (`conversations.replies`). Returns messages in
chronological order. Requires the `*:history` scope for the channel type
(e.g. `channels:history` for public channels — already in `oauth_defaults`).

**Input:**
```json
{
  "channel": "C1234567890",
  "ts": "1700000001.000000",
  "limit": 200
}
```

`ts` is the parent thread timestamp. `limit` defaults to 200 (max 200). `cursor`
supports pagination.

**Output:**
```json
{
  "messages": [
    {"text": "Original message", "user": "U001", "ts": "1700000001.000000", "thread_ts": "1700000001.000000"},
    {"text": "Reply from colleague", "user": "U002", "ts": "1700000002.000000", "thread_ts": "1700000001.000000"}
  ],
  "next_cursor": "",
  "has_more": false
}
```

### `read_history`

Read recent messages from a Slack channel (`conversations.history`). Requires
the `*:history` scope for the channel type (already in `oauth_defaults`).

**Input:**
```json
{
  "channel": "C1234567890",
  "limit": 100,
  "oldest": "1700000000.000000"
}
```

`limit` defaults to 100 (max 200). `oldest` restricts to messages after that
timestamp (exclusive). `cursor` supports pagination.

**Output:**
```json
{
  "messages": [
    {"text": "Recent message", "user": "U001", "ts": "1700000010.000000"}
  ],
  "next_cursor": "",
  "has_more": false
}
```

### `update_message`

Update (edit) a previously posted Slack message. Supports optional Block Kit
blocks. Same `text`/`blocks` rules as `post_message`.

**Input:**
```json
{
  "channel": "C1234567890",
  "ts": "1700000001.000000",
  "text": "Updated message text"
}
```

**Output:**
```json
{"channel": "C1234567890", "ts": "1700000001.000000", "text": "Updated message text"}
```

The `text` key is always present in the output even when Slack echoes an empty string.

### `delete_message`

Delete a Slack message posted by the bot.

**Input:**
```json
{"channel": "C1234567890", "ts": "1700000001.000000"}
```

**Output:**
```json
{"channel": "C1234567890", "ts": "1700000001.000000"}
```

### `lookup_user`

Look up a Slack user by ID. Returns display fields for personalization.

**Input:**
```json
{"user": "U012AB3CD"}
```

**Output:**
```json
{"id": "U012AB3CD", "name": "alice", "real_name": "Alice Smith", "tz": "America/New_York"}
```

### Planned

- **`upload_file`** (`files:write`) — share logs, reports, and images. Deferred
  to a follow-up PR because it is the only new tool requiring the `files:write`
  scope, which would force existing installs to re-authorize. All other tools
  above require no new scopes.

## Required Slack scopes

The following OAuth 2.0 bot token scopes must be granted when installing the
Slack app. These are declared in `manifest.go`'s `OAuthDefaults.Scopes` list
and shown in the admin UI during install.

| Scope             | Used by |
|-------------------|---------|
| `channels:history` | `read_thread`, `read_history` (public channels) |
| `channels:read`   | `list_channels` (public channels) |
| `chat:write`      | `post_message`, `update_message`, `delete_message` |
| `groups:history`  | `read_thread`, `read_history` (private channels) |
| `im:history`      | `read_thread`, `read_history` (DMs) |
| `im:write`        | `post_message` to DMs |
| `mpim:history`    | `read_thread`, `read_history` (multi-party DMs) |
| `users:read`      | `lookup_user` and user lookup in various responses |

All scopes listed above were already in `oauth_defaults` before this feature
shipped — existing installs do **not** need to re-authorize to use the new tools.
The only planned scope addition (`files:write` for `upload_file`) is deferred to
a separate PR to keep re-auth impact isolated.

### Optional scope — `groups:read` (private channels in the searchable picker)

The searchable channel picker (audience entries, trigger bindings, and config
fields backed by the `channels` option provider) calls `conversations.list`
requesting **both public and private channels**. Listing private channels
requires the `groups:read` scope, which is intentionally **not** in
`oauth_defaults` to avoid forcing existing installs to re-authorize.

| Scope         | Used by |
|---------------|---------|
| `groups:read` | `channels` option provider — listing private channels in the searchable picker |

Without `groups:read`, Slack rejects the **entire** `conversations.list` call
with `missing_scope` (not just the private results), so the picker shows no
options and the field degrades to free-text "enter an ID directly". To enable
it: add `groups:read` to the Slack app's bot scopes under **OAuth &
Permissions**, add it to `oauth_defaults.scopes` in `manifest.go` (so the
authorize URL requests it), reinstall the rebuilt bundle, and **re-authorize**
the instance. If you only use public channels, no change is needed — public
channels are already covered by `channels:read`.

## Token refresh

Slack v2 bot tokens (`xoxb-` prefix) do not expire under normal circumstances
— they remain valid indefinitely unless explicitly revoked by a workspace admin
or the app is uninstalled. As a result, the Phase-7 credential refresh scanner
has nothing to refresh for Slack in the steady state. The `auth_expired` health
transition fires only when a token is revoked: the next `ToolService.Call`
invocation runs `auth.test`, receives `invalid_auth` or `token_revoked`, and
sets the plugin to `UNHEALTHY`. The operator then re-authorizes via the admin UI
to restore the healthy state.

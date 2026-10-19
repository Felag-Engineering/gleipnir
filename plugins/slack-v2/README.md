# slack-v2

The Slack plugin rebuilt as a containerized MCP server (ADR-053..ADR-060). It
serves the nine v1 tools: four read tools and five write tools. Channel and
event features follow in later issues.

**The v1 plugin (`plugins/slack`) stays live until the flip (#1005).** Nothing
here replaces or changes it, and the two are never run side by side: the v2
substrate only runs in a `-tags substratev2` build.

## Tools and scopes

Each tool needs the Slack OAuth scopes below. Where two are listed, the tool
needs one of them depending on the kind of conversation it targets. The
manifest's `auth.oauth_defaults.scopes` is the union, which a test enforces.

| Tool | Slack method | Scopes |
|------|--------------|--------|
| `list_channels` | `conversations.list` | `channels:read` (public), `groups:read` (private) |
| `read_history` | `conversations.history` | `channels:history` (public), `groups:history` (private) |
| `read_thread` | `conversations.replies` | `channels:history` (public), `groups:history` (private) |
| `lookup_user` | `users.info` | `users:read` |
| `post_message` | `chat.postMessage` | `chat:write` |
| `update_message` | `chat.update` | `chat:write` |
| `delete_message` | `chat.delete` | `chat:write` |
| `react` | `reactions.add` | `reactions:write` |
| `set_topic` | `conversations.setTopic` | `channels:manage` (public), `groups:write` (private) |

DMs and group DMs (`im:*`, `mpim:*`) are deliberately not requested yet.

`post_message` and `update_message` take `text`, Block Kit `blocks` (a JSON
array, passed to Slack untouched), or both; `text` is then the notification
fallback. Input that cannot be valid (a missing channel or timestamp, neither
text nor blocks, `blocks` that is not an array, a topic over 250 characters) is
refused with an `isError` result before Slack is called.

## Per-capability health

When Slack answers a tool call with `missing_scope` or `not_allowed_token_type`,
the plugin calls the host's `host/set_health_state` for profile `tool_provider`
and that one tool as the capability, state `unhealthy`, with a detail naming the
scope. Other tools stay callable and healthy. The host clears the fault when it
observes the container restart (a plugin can only mark itself worse), so the
report is sent once per process per tool. The tool call itself returns an
`isError` result telling the operator which scope to add.

An instance with no credentials returns an error result but reports no health:
that state belongs to the host.

## Asking questions (`channel/request`)

A request is posted as one button per option and returned as a durable task.
The task and the `{channel, ts}` of its message live under `DATA_DIR`, so a
restart does not lose the wait. A form-only request (`requestedSchema` with no
options) is refused so the audience falls through to in-app; `kind: permission`
colours conventional approve/deny option ids. `tasks/cancel` rewrites the
message to a resolved summary without buttons (a cancel that lost to a
completion changes nothing).

## Environment

| Variable | Default | Purpose |
|----------|---------|---------|
| `GLEIPNIR_HOST_ENDPOINT_URL` | set by the host | Host endpoint for credentials and health |
| `GLEIPNIR_INSTANCE_TOKEN` | set by the host | Bearer token for the host endpoint |
| `DATA_DIR` | `/data` | Instance volume for durable `channel/request` tasks (`tasks/`) and the record of each posted message (`messages/`). The image creates it owned by uid 65532; mount a volume there for tasks to outlive the container |
| `SLACK_API_URL` | `https://slack.com/api/` | Slack Web API base. Point it at a fake Slack in tests; egress still bounds what the container can reach |

The credentials blob is fetched on every call and the Slack client is cached by
access token, so a token the host rotates is picked up on its next use.

## Build, sign, test

```sh
cd plugins/slack-v2
go test -race ./...

make image                                    # build the container image
make archive                                  # save it as an OCI archive
export GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE=...
make bundle SIGNING_KEY=/path/outside/repo/signing.key   # -> ../../dist/*.tar.gz
```

The signing key is held outside the repository (#947); nothing here generates or
stores one. For a throwaway bundle, create an ephemeral key with
`gleipnir-plugin keygen` into a temp directory, as `scripts/build-conformance-stub.sh`
does.

`manifest.yaml` carries a placeholder image digest. `make bundle` stamps the
archive's real config digest into a copy and signs that.

## tools/list golden

`testdata/tools_list.golden.json` pins the `tools/list` result. A test here
fails when the tools drift; regenerate with
`go test -run TestToolsListMatchesGolden -update`. The root module's
`internal/schemanorm` test reads the same file and runs each `inputSchema`
through `schemanorm.Normalize`, because the host canonicalizes tool schemas at
discovery and a rejected schema would otherwise only show up at install time.

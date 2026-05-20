# Gleipnir E2E Playwright Tests

## What this runs

The kitchen-sink acceptance test against a real Slack workspace. The spec
drives the full Slack plugin flow end-to-end:

1. **TriggerService ingress** — posts a message to a Slack channel; the plugin
   delivers it via Socket Mode and fires a policy run.
2. **ToolService** — the agent calls `<instance>.post_message` to reply.
3. **ChannelService egress** — the policy audience delivers a `Notify` back to
   Slack.

All three service grants are validated in one test run (`slack-kitchen-sink.spec.ts`).

The spec self-skips (`test.skip()`) when any required environment variable is
absent, so running `npm test` without secrets is always safe and produces a
clean "skipped" result instead of an error.

## Required environment variables

| Variable | Description |
|---|---|
| `SLACK_TEST_CLIENT_ID` | Slack app Client ID (from Basic Information → App Credentials) |
| `SLACK_TEST_CLIENT_SECRET` | Slack app Client Secret |
| `SLACK_TEST_BOT_TOKEN` | Bot token (`xoxb-…`) for posting messages and reading history |
| `SLACK_TEST_APP_TOKEN` | App-level token (`xapp-…`) for Socket Mode |
| `SLACK_TEST_CHANNEL_ID` | Channel ID for the dedicated `#gleipnir-e2e` channel (e.g. `C012ABCDEF`) |
| `GLEIPNIR_E2E_BASE_URL` | Base URL of the running Gleipnir instance (default: `http://localhost:8080`) |
| `GLEIPNIR_E2E_ANTHROPIC_API_KEY` | Anthropic API key used to configure the LLM model for the test run |
| `GLEIPNIR_E2E_ADMIN_USER` | Admin username created by the setup wizard |
| `GLEIPNIR_E2E_ADMIN_PASSWORD` | Admin password |

**Recommendation:** create a dedicated `#gleipnir-e2e` Slack channel for this
test. The spec posts 2 messages per run (1 ingress trigger + 1 agent reply);
over 365 nightly runs that is ~730 messages/year.

## Running locally

```bash
cd tests/playwright
npm install
npx playwright install --with-deps chromium
npm test
```

The spec will skip cleanly if any required env var is absent.

**Pre-requisite:** the Slack plugin must already be installed and named
`slack-e2e` before running. See Steps 1–4 in
`docs/developer/manual-testing.md §"Slack plugin (OAuth authcode)"` for the
one-time setup. The spec does not install or OAuth-authorize the plugin — those
are one-time steps.

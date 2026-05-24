import { test, expect, request } from '@playwright/test';

// All five Slack secrets plus Gleipnir admin credentials are required.
// The spec self-skips when any are absent so forks and local runs without
// secrets produce a clean skip instead of a cascading failure.
const required = [
  'SLACK_TEST_CLIENT_ID',
  'SLACK_TEST_CLIENT_SECRET',
  'SLACK_TEST_BOT_TOKEN',
  'SLACK_TEST_APP_TOKEN',
  'SLACK_TEST_CHANNEL_ID',
  'GLEIPNIR_E2E_ANTHROPIC_API_KEY',
  'GLEIPNIR_E2E_ADMIN_USER',
  'GLEIPNIR_E2E_ADMIN_PASSWORD',
];

// A unique suffix embedded in the agent task prompt and asserted in the Slack
// channel history. Prevents cross-run collisions in the shared channel.
const replyToken = `GLEIPNIR_E2E_REPLY_TOKEN_${Date.now()}`;

let policyID: string | null = null;

test.beforeAll(() => {
  test.skip(
    required.some((k) => !process.env[k]),
    'missing required env vars — skipping kitchen-sink spec',
  );
});

// Clean up the policy after each test even if the test fails partway through.
test.afterEach(async () => {
  if (!policyID) return;
  const ctx = await request.newContext({
    baseURL: process.env.GLEIPNIR_E2E_BASE_URL || 'http://localhost:8080',
  });
  // Best-effort: ignore errors during cleanup.
  await ctx.delete(`/api/v1/policies/${policyID}`).catch(() => {});
  policyID = null;
});

test('kitchen sink: slack → trigger → run → slack reply', async ({ request: _page }) => {
  const base = process.env.GLEIPNIR_E2E_BASE_URL || 'http://localhost:8080';
  const ctx = await request.newContext({ baseURL: base });

  // ── Step 1: Authenticate ────────────────────────────────────────────────────
  const loginResp = await ctx.post('/api/v1/auth/login', {
    data: {
      username: process.env.GLEIPNIR_E2E_ADMIN_USER,
      password: process.env.GLEIPNIR_E2E_ADMIN_PASSWORD,
    },
  });
  expect(loginResp.ok(), `login failed: ${await loginResp.text()}`).toBe(true);

  // ── Step 2: Configure the system default LLM model ─────────────────────────
  // Gap 1: correct URL and body format for setting the Anthropic provider key.
  const keyResp = await ctx.put('/api/v1/admin/providers/anthropic/key', {
    data: { key: process.env.GLEIPNIR_E2E_ANTHROPIC_API_KEY },
  });
  expect(keyResp.ok(), `provider key config failed: ${await keyResp.text()}`).toBe(true);

  // Gap 2: explicitly set the default model (setting the key does NOT auto-populate it).
  const defaultModelResp = await ctx.put('/api/v1/admin/settings/default-model', {
    data: { provider: 'anthropic', name: 'claude-sonnet-4-6' },
  });
  expect(defaultModelResp.ok(), `set default model failed: ${await defaultModelResp.text()}`).toBe(true);

  const configResp = await ctx.get('/api/v1/config');
  expect(configResp.ok()).toBe(true);
  const config = await configResp.json();
  expect(config.data?.default_model, 'default_model must be set').toBeTruthy();

  // ── Step 3: Locate the slack-e2e plugin instance ───────────────────────────
  // Gap 3: correct endpoint for listing plugin instances (flat array).
  const pluginsResp = await ctx.get('/api/v1/admin/plugin-instances');
  expect(pluginsResp.ok(), `list plugin instances failed: ${await pluginsResp.text()}`).toBe(true);

  // Gap 4: flat array — each entry carries id, plugin_id, instance_name, etc.
  const instances = (await pluginsResp.json()).data ?? [];
  const slackInstance = instances.find(
    (i: { instance_name: string }) => i.instance_name === 'slack-e2e',
  );
  expect(slackInstance, 'slack-e2e plugin instance must exist (pre-installed by workflow)').toBeTruthy();
  const instanceID = slackInstance.id;
  const pluginID = slackInstance.plugin_id;

  // ── Step 4: Seed instance credentials ──────────────────────────────────────
  // Gap 5: use the new oauth-token endpoint with the correct body shape.
  const credResp = await ctx.put(
    `/api/v1/admin/plugins/${pluginID}/instances/${instanceID}/credentials/oauth-token`,
    { data: { access_token: process.env.SLACK_TEST_BOT_TOKEN } },
  );
  expect(credResp.ok(), `set oauth token failed: ${await credResp.text()}`).toBe(true);

  // Gap 6: use the new instance-config endpoint; wrap payload in {config, expected_version}.
  // expected_version is 1 (not 0) because the oauth-token PUT above already bumped
  // the row's version from 0 → 1. The oauth-token PUT returns 204 No Content so
  // we cannot extract the version from its response body — hardcoded 1 is correct
  // for this controlled E2E setup where no concurrent writers exist.
  const cfgResp = await ctx.put(
    `/api/v1/admin/plugins/${pluginID}/instances/${instanceID}/config`,
    {
      data: {
        config: { app_level_token: process.env.SLACK_TEST_APP_TOKEN },
        expected_version: 1,
      },
    },
  );
  expect(cfgResp.ok(), `set config failed: ${await cfgResp.text()}`).toBe(true);

  // Re-fetch the instance to get the latest version before writing subscription
  // scope. Between the config PUT response and now, the plugin subprocess may
  // have called SetHealthState via hostsvc (bumping the version). Using the
  // config PUT response version directly would risk a 409 on a stale version.
  const freshInstResp = await ctx.get(
    `/api/v1/admin/plugins/${pluginID}/instances/${instanceID}`,
  );
  expect(freshInstResp.ok(), `re-fetch instance failed: ${await freshInstResp.text()}`).toBe(true);
  const freshInst = await freshInstResp.json();
  const scopeVersion: number = freshInst.data?.version;

  // The supervisor gates on isEmptyScope: when subscription_scope_json is "{}"
  // (the CreatePluginInstance default) it skips opening the TriggerService stream
  // entirely. Without a non-empty scope, no Socket Mode connection opens and no
  // Slack events reach the dispatcher.
  const channelID = process.env.SLACK_TEST_CHANNEL_ID!;
  const scopeResp = await ctx.put(
    `/api/v1/admin/plugins/${pluginID}/instances/${instanceID}/subscription-scope`,
    {
      data: {
        scope: { channels: [channelID] },
        expected_version: scopeVersion,
      },
    },
  );
  expect(scopeResp.ok(), `set subscription scope failed: ${await scopeResp.text()}`).toBe(true);

  // Poll until the instance becomes healthy (deadline: 30s).
  const healthDeadline = Date.now() + 30_000;
  let healthy = false;
  while (Date.now() < healthDeadline) {
    const hResp = await ctx.get(`/api/v1/admin/plugins/${pluginID}/instances/${instanceID}`);
    if (hResp.ok()) {
      const body = await hResp.json();
      // Gap 7: the field is `state`, not `health_state`.
      if (body.data?.state === 'healthy') {
        healthy = true;
        break;
      }
    }
    await new Promise((r) => setTimeout(r, 2_000));
  }
  expect(healthy, 'instance never reached state=healthy within 30s').toBe(true);

  // ── Step 5: Create the kitchen-sink policy ─────────────────────────────────
  const policyYAML = `\
name: slack-e2e-kitchen-sink
trigger:
  type: subscribed
  source: slack-e2e
  event_kind: channel_message
  binding:
    channel: "${channelID}"
capabilities:
  tools:
    - tool: slack-e2e.post_message
audience:
  - plugin: slack-e2e
    notify: true
    channel_config:
      channel: "${channelID}"
agent:
  task: |
    You have received a Slack message. Your only job is to call post_message
    exactly once and post the following literal string to channel ${channelID}:
    ${replyToken}
    Do not paraphrase. Do not add anything. Post exactly that string.
  limits:
    max_tool_calls_per_run: 3
  concurrency: skip
`;

  const createPolicyResp = await ctx.post('/api/v1/policies', {
    data: { yaml: policyYAML },
  });
  expect(createPolicyResp.ok(), `create policy failed: ${await createPolicyResp.text()}`).toBe(true);
  const createdPolicy = await createPolicyResp.json();
  policyID = createdPolicy.data?.id;
  expect(policyID, 'created policy must have an ID').toBeTruthy();

  // ── Step 6: Trigger ingress — post a message to the Slack channel ──────────
  const beforeTriggerTs = (Date.now() / 1000 - 1).toFixed(6);
  const slackPostResp = await fetch('https://slack.com/api/chat.postMessage', {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${process.env.SLACK_TEST_BOT_TOKEN}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      channel: channelID,
      text: `Gleipnir e2e ping ${Date.now()}`,
    }),
  });
  const slackPost = await slackPostResp.json();
  expect(slackPost.ok, `chat.postMessage failed: ${slackPost.error}`).toBe(true);

  // ── Step 7: Wait for the run to fire and complete ──────────────────────────
  // Deadline: 240s (covers agent turn + Slack API latency + Anthropic retry).
  const runDeadline = Date.now() + 240_000;
  let completedRunID: string | undefined;
  while (Date.now() < runDeadline) {
    const runsResp = await ctx.get(`/api/v1/runs?policy_id=${policyID}&limit=1`);
    if (runsResp.ok()) {
      const runsBody = await runsResp.json();
      const runs: Array<{ id: string; status: string }> = runsBody.data ?? [];
      const done = runs.find((r) => r.status === 'complete');
      if (done) {
        completedRunID = done.id;
        break;
      }
    }
    await new Promise((r) => setTimeout(r, 5_000));
  }
  expect(completedRunID, 'a completed run must appear within 240s').toBeTruthy();

  // ── Step 8a: Assert TriggerService ingress (run exists, trigger=subscribed) ─
  const runResp = await ctx.get(`/api/v1/runs/${completedRunID}`);
  expect(runResp.ok()).toBe(true);
  const runBody = await runResp.json();
  expect(runBody.data?.trigger_type, 'trigger_type must be subscribed').toBe('subscribed');

  // ── Step 8b: Assert ToolService.post_message was called ───────────────────
  const stepsResp = await ctx.get(`/api/v1/runs/${completedRunID}/steps`);
  expect(stepsResp.ok()).toBe(true);
  const stepsBody = await stepsResp.json();
  const steps: Array<{ type: string; content: string }> = stepsBody.data ?? [];

  const toolCallStep = steps.find(
    (s) => s.type === 'tool_call' && s.content.includes('post_message'),
  );
  expect(toolCallStep, 'a tool_call step for post_message must exist').toBeTruthy();

  const toolResultStep = steps.find(
    (s) => s.type === 'tool_result' && s.content.includes('"ok"'),
  );
  expect(toolResultStep, 'a successful tool_result step must follow the post_message call').toBeTruthy();

  // ── Step 8c: Assert ChannelService.Notify egress — reply in channel ─────────
  // Use conversations.history (no indexer lag unlike search.messages).
  const histDeadline = Date.now() + 60_000;
  let foundReply = false;
  while (Date.now() < histDeadline) {
    const histResp = await fetch(
      `https://slack.com/api/conversations.history?channel=${channelID}&oldest=${beforeTriggerTs}&limit=50`,
      {
        headers: { Authorization: `Bearer ${process.env.SLACK_TEST_BOT_TOKEN}` },
      },
    );
    const hist = await histResp.json();
    if (hist.ok) {
      const messages: Array<{ text: string }> = hist.messages ?? [];
      if (messages.some((m) => m.text.includes(replyToken))) {
        foundReply = true;
        break;
      }
    }
    await new Promise((r) => setTimeout(r, 3_000));
  }
  expect(
    foundReply,
    `Slack channel must contain a message with ${replyToken} within 60s`,
  ).toBe(true);
});

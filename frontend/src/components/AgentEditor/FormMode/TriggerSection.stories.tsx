import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { http, HttpResponse } from 'msw';
import '@/tokens.css';
import { TriggerSection } from './TriggerSection';
import type { TriggerFormState } from './types';
import type { ApiPluginInstanceForAudience } from '@/api/types';
import decoratorStyles from './TriggerSection.stories.module.css';

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

// --- Fixtures ---

const SLACK_INSTANCE: ApiPluginInstanceForAudience = {
  id: 'inst-1',
  plugin_id: 'plugin-slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  implements_notify: true,
  implements_request: false,
  config_schema: null,
  event_kinds: [
    { kind: 'channel_message', description: 'A message posted in a channel' },
    { kind: 'direct_message', description: 'A direct message to the bot' },
  ],
};

const SLACK_WITH_EXAMPLES: ApiPluginInstanceForAudience = {
  ...SLACK_INSTANCE,
  event_kinds: [
    {
      kind: 'channel_message',
      description: 'A message posted in a channel',
      binding_schema: {
        type: 'object',
        properties: {
          channel: { type: 'string' },
        },
      },
      examples: [
        { name: 'incident', payload: { channel: '#incidents', text: 'alert' } },
        { name: 'general', payload: { channel: '#general', text: 'hello' } },
      ],
    },
    { kind: 'direct_message', description: 'A direct message to the bot' },
  ],
};

// Handlers shared across stories that need a config endpoint
const CONFIG_HANDLER = http.get('/api/v1/config', () =>
  HttpResponse.json({ data: { public_url: '', default_model: null } }),
);

const NO_PLUGIN_INSTANCES = http.get('/api/v1/admin/plugin-instances', () =>
  HttpResponse.json({ data: [] }),
);

const WITH_SLACK_HANDLER = http.get('/api/v1/admin/plugin-instances', () =>
  HttpResponse.json({ data: [SLACK_INSTANCE] }),
);

const WITH_SLACK_EXAMPLES_HANDLER = http.get('/api/v1/admin/plugin-instances', () =>
  HttpResponse.json({ data: [SLACK_WITH_EXAMPLES] }),
);

// --- Meta ---

const meta: Meta<typeof TriggerSection> = {
  title: 'PolicyEditor/FormMode/TriggerSection',
  component: TriggerSection,
  decorators: [
    (Story) => {
      // Fresh query client per story to prevent cached query state bleeding across stories.
      const qc = makeQueryClient();
      return (
        <QueryClientProvider client={qc}>
          <div className={decoratorStyles.decorator}>
            <Story />
          </div>
        </QueryClientProvider>
      );
    },
  ],
};

export default meta;
type Story = StoryObj<typeof TriggerSection>;

// --- Stories ---

export const WebhookSelected: Story = {
  parameters: {
    msw: { handlers: [CONFIG_HANDLER, NO_PLUGIN_INSTANCES] },
  },
  args: {
    value: { type: 'webhook', auth: 'hmac' },
    policyId: 'abc-123',
    onChange: fn(),
  },
};

export const WebhookNewAgent: Story = {
  parameters: {
    msw: { handlers: [CONFIG_HANDLER, NO_PLUGIN_INSTANCES] },
  },
  args: {
    value: { type: 'webhook', auth: 'hmac' },
    onChange: fn(),
  },
};

export const ManualSelected: Story = {
  parameters: {
    msw: { handlers: [CONFIG_HANDLER, NO_PLUGIN_INSTANCES] },
  },
  args: {
    value: { type: 'manual' },
    policyId: 'manual-policy',
    onChange: fn(),
  },
};

export const CronSelected: Story = {
  parameters: {
    msw: { handlers: [CONFIG_HANDLER, NO_PLUGIN_INSTANCES] },
  },
  args: {
    value: { type: 'cron', cronExpr: '0 9 * * 1' },
    policyId: 'cron-policy',
    onChange: fn(),
  },
};

export const PollSelected: Story = {
  parameters: {
    msw: { handlers: [CONFIG_HANDLER, NO_PLUGIN_INSTANCES] },
  },
  args: {
    value: {
      type: 'poll',
      interval: '5m',
      match: 'all',
      checks: [
        { tool: 'monitor.check_status', input: '', path: '$.status', comparator: 'equals', value: 'degraded' },
      ],
    },
    policyId: 'poll-policy',
    onChange: fn(),
  },
};

export const PollMultipleChecks: Story = {
  parameters: {
    msw: { handlers: [CONFIG_HANDLER, NO_PLUGIN_INSTANCES] },
  },
  args: {
    value: {
      type: 'poll',
      interval: '10m',
      match: 'any',
      checks: [
        { tool: 'monitor.check_status', input: '', path: '$.status', comparator: 'equals', value: 'degraded' },
        { tool: 'monitor.check_count', input: '{"env":"prod"}', path: '$.error_count', comparator: 'greater_than', value: '10' },
      ],
    },
    policyId: 'poll-multi-policy',
    onChange: fn(),
  },
};

// SubscribedSelected — shows the binding form for a plugin event trigger (no examples).
export const SubscribedSelected: Story = {
  parameters: {
    msw: { handlers: [CONFIG_HANDLER, WITH_SLACK_HANDLER] },
  },
  args: {
    value: { type: 'subscribed', source: 'slack-prod', eventKind: 'channel_message', binding: {} },
    policyId: 'subscribed-policy',
    onChange: fn(),
  },
};

// SubscribedWithTestButton — plugin declares examples, test button is enabled.
// MSW returns mixed match results when the button is clicked.
export const SubscribedWithTestButton: Story = {
  parameters: {
    msw: {
      handlers: [
        CONFIG_HANDLER,
        WITH_SLACK_EXAMPLES_HANDLER,
        http.post(
          '/api/v1/admin/plugin-instances/inst-1/event-kinds/channel_message/test-binding',
          () =>
            HttpResponse.json({
              data: { results: [{ match: true }, { match: false }] },
            }),
        ),
      ],
    },
  },
  args: {
    value: { type: 'subscribed', source: 'slack-prod', eventKind: 'channel_message', binding: { channel: '#incidents' } },
    policyId: 'subscribed-examples-policy',
    onChange: fn(),
  },
};

function InteractiveTriggerSection() {
  const [value, setValue] = useState<TriggerFormState>({ type: 'webhook', auth: 'hmac' });
  return <TriggerSection value={value} onChange={setValue} policyId="example-policy" />;
}

export const Interactive: Story = {
  parameters: {
    msw: { handlers: [CONFIG_HANDLER, WITH_SLACK_HANDLER] },
  },
  render: () => <InteractiveTriggerSection />,
};

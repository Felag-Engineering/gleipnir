import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { http, HttpResponse } from 'msw';
import '@/tokens.css';
import { SubscribedBindingConfig } from './SubscribedBindingConfig';
import type { ApiPluginInstanceForAudience } from '@/api/types';
import decoratorStyles from '../TriggerSection.stories.module.css';

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

// --- Fixtures ---

const SLACK_INSTANCE: ApiPluginInstanceForAudience = {
  id: 'inst-slack',
  plugin_id: 'plugin-slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  implements_notify: true,
  implements_request: false,
  config_schema: null,
  event_kinds: [
    {
      kind: 'channel_message',
      description: 'A message posted in a channel',
      binding_schema: {
        type: 'object',
        properties: {
          channel: { type: 'string', description: 'Channel name (exact match)' },
          pattern: { type: 'string', format: 'regex', description: 'Text pattern (RE2)' },
          mention_only: { type: 'boolean', description: 'Only fire when mentioned' },
        },
      },
      examples: [
        { name: 'incident-alert', payload: { channel: '#incidents', text: 'P1 fire', mentioned: false } },
        { name: 'mentions-bot', payload: { channel: '#general', text: 'hey @bot', mentioned: true } },
        { name: 'no-match', payload: { channel: '#random', text: 'hello' } },
      ],
    },
  ],
};

const SLACK_NO_EXAMPLES: ApiPluginInstanceForAudience = {
  ...SLACK_INSTANCE,
  id: 'inst-no-ex',
  instance_name: 'slack-no-examples',
  event_kinds: [
    {
      kind: 'channel_message',
      description: 'A message posted in a channel',
      binding_schema: SLACK_INSTANCE.event_kinds![0].binding_schema,
      examples: [],
    },
  ],
};

const TEST_ENDPOINT = '/api/v1/admin/plugin-instances/inst-slack/event-kinds/channel_message/test-binding';

// --- Meta ---

const meta: Meta<typeof SubscribedBindingConfig> = {
  title: 'PolicyEditor/FormMode/triggers/SubscribedBindingConfig',
  component: SubscribedBindingConfig,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <div className={decoratorStyles.decorator}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof SubscribedBindingConfig>;

// --- Stories ---

// Default: mixed match results — first matches, second matches, third does not.
export const Default: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post(TEST_ENDPOINT, () =>
          HttpResponse.json({
            data: {
              results: [
                { match: true },
                { match: true },
                { match: false },
              ],
            },
          }),
        ),
      ],
    },
  },
  args: {
    source: 'slack-prod',
    eventKind: 'channel_message',
    binding: { channel: '#incidents' },
    onChange: fn(),
    pluginInstances: [SLACK_INSTANCE],
  },
};

// AllMatch: every example matches.
export const AllMatch: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post(TEST_ENDPOINT, () =>
          HttpResponse.json({
            data: {
              results: [
                { match: true },
                { match: true },
                { match: true },
              ],
            },
          }),
        ),
      ],
    },
  },
  args: {
    source: 'slack-prod',
    eventKind: 'channel_message',
    binding: {},
    onChange: fn(),
    pluginInstances: [SLACK_INSTANCE],
  },
};

// NoExamples: button is disabled, tooltip visible.
export const NoExamples: Story = {
  parameters: {
    msw: { handlers: [] },
  },
  args: {
    source: 'slack-no-examples',
    eventKind: 'channel_message',
    binding: { channel: '#incidents' },
    onChange: fn(),
    pluginInstances: [SLACK_NO_EXAMPLES],
  },
};

// CompileError: server returns 400 with a compile error detail.
export const CompileError: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post(TEST_ENDPOINT, () =>
          HttpResponse.json(
            { error: 'binding compile error', detail: 'binding: invalid regular expression (Go RE2 required): field "pattern": error parsing regexp' },
            { status: 400 },
          ),
        ),
      ],
    },
  },
  args: {
    source: 'slack-prod',
    eventKind: 'channel_message',
    binding: { pattern: '[invalid(' },
    onChange: fn(),
    pluginInstances: [SLACK_INSTANCE],
  },
};

// LoadingResults: the mutation is in-flight (simulated via a slow response).
export const LoadingResults: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post(TEST_ENDPOINT, async () => {
          await new Promise((r) => setTimeout(r, 60_000));
          return HttpResponse.json({ data: { results: [] } });
        }),
      ],
    },
  },
  args: {
    source: 'slack-prod',
    eventKind: 'channel_message',
    binding: { channel: '#incidents' },
    onChange: fn(),
    pluginInstances: [SLACK_INSTANCE],
  },
};

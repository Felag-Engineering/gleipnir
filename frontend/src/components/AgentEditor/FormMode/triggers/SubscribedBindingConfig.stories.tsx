import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
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
  version: 0,
  event_kinds: [
    {
      kind: 'channel_message',
      description: 'A message posted in a channel',
      guidance: "A human posts a message in a channel the instance is watching. The bot's own posts are dropped by the self-trigger guard. Uses the instance's channel watch scope — no extra subscription toggle.",
      binding_schema: {
        type: 'object',
        properties: {
          channel_id: {
            type: 'string',
            title: 'Channel',
            description: 'Exact match on the Slack channel. Pick a channel from the searchable list; matches the message\'s stable channel ID (e.g. C012AB3CD).',
            'x-gleipnir-options': { source: 'channels' },
          },
          text: {
            type: 'string',
            format: 'contains',
            title: 'Text contains',
            description: 'Case-sensitive substring; matches anywhere in the message body (not anchored to the start).',
          },
          mention_only: {
            type: 'boolean',
            title: 'Mention-only',
            description: 'Fire only when the bot is explicitly @-mentioned.',
          },
        },
      },
      examples: [
        { name: 'incident-alert', payload: { channel_id: 'C09INCIDENT', text: 'P1 fire', mentioned: false } },
        { name: 'mentions-bot', payload: { channel_id: 'C012ABCDEF', text: 'hey @bot', mentioned: true } },
        { name: 'no-match', payload: { channel_id: 'CRANDOM123', text: 'hello' } },
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
const OPTIONS_ENDPOINT = '/api/v1/admin/plugins/plugin-slack/instances/inst-slack/options/channels';

// --- Meta ---

const meta: Meta<typeof SubscribedBindingConfig> = {
  title: 'PolicyEditor/FormMode/triggers/SubscribedBindingConfig',
  component: SubscribedBindingConfig,
  decorators: [
    (Story) => (
      <MemoryRouter>
        <QueryClientProvider client={makeQueryClient()}>
          <div className={decoratorStyles.decorator}>
            <Story />
          </div>
        </QueryClientProvider>
      </MemoryRouter>
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
        http.get(OPTIONS_ENDPOINT, () =>
          HttpResponse.json({
            data: {
              options: [
                { value: 'C09INCIDENT', label: '#incidents' },
                { value: 'C012ABCDEF', label: '#general' },
              ],
              next_cursor: '',
            },
          }),
        ),
      ],
    },
  },
  args: {
    source: 'slack-prod',
    eventKind: 'channel_message',
    binding: { channel_id: 'C09INCIDENT' },
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
        http.get(OPTIONS_ENDPOINT, () =>
          HttpResponse.json({
            data: {
              options: [
                { value: 'C09INCIDENT', label: '#incidents' },
                { value: 'C012ABCDEF', label: '#general' },
              ],
              next_cursor: '',
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
    msw: {
      handlers: [
        http.get(OPTIONS_ENDPOINT, () =>
          HttpResponse.json({
            data: { options: [], next_cursor: '' },
          }),
        ),
      ],
    },
  },
  args: {
    source: 'slack-no-examples',
    eventKind: 'channel_message',
    binding: { channel_id: 'C09INCIDENT' },
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
            { error: 'binding compile error', detail: 'binding: invalid regular expression (Go RE2 required): field "text": error parsing regexp' },
            { status: 400 },
          ),
        ),
        http.get(OPTIONS_ENDPOINT, () =>
          HttpResponse.json({
            data: { options: [], next_cursor: '' },
          }),
        ),
      ],
    },
  },
  args: {
    source: 'slack-prod',
    eventKind: 'channel_message',
    binding: { text: '[invalid(' },
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
        http.get(OPTIONS_ENDPOINT, () =>
          HttpResponse.json({
            data: {
              options: [{ value: 'C012ABCDEF', label: '#incidents' }],
              next_cursor: '',
            },
          }),
        ),
      ],
    },
  },
  args: {
    source: 'slack-prod',
    eventKind: 'channel_message',
    binding: { channel_id: 'C09INCIDENT' },
    onChange: fn(),
    pluginInstances: [SLACK_INSTANCE],
  },
};

import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import '@/tokens.css';
import { TriggerSection } from './TriggerSection';
import type { TriggerFormState } from './types';
import decoratorStyles from './TriggerSection.stories.module.css';

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

const MOCK_CONFIG_RESPONSE = JSON.stringify({ data: { public_url: '', default_model: null } });

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
  beforeEach: () => {
    const originalFetch = window.fetch;
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string'
        ? input
        : input instanceof URL
          ? input.toString()
          : input.url;
      if (url.includes('/api/v1/config')) {
        return new Response(MOCK_CONFIG_RESPONSE, {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      return originalFetch(input, init);
    };
    return () => { window.fetch = originalFetch; };
  },
};

export default meta;
type Story = StoryObj<typeof TriggerSection>;

export const WebhookSelected: Story = {
  args: {
    value: { type: 'webhook', auth: 'hmac' },
    policyId: 'abc-123',
    onChange: fn(),
  },
};

export const WebhookNewAgent: Story = {
  args: {
    value: { type: 'webhook', auth: 'hmac' },
    onChange: fn(),
  },
};

export const ManualSelected: Story = {
  args: {
    value: { type: 'manual' },
    policyId: 'manual-policy',
    onChange: fn(),
  },
};

export const CronSelected: Story = {
  args: {
    value: { type: 'cron', cronExpr: '0 9 * * 1' },
    policyId: 'cron-policy',
    onChange: fn(),
  },
};

export const PollSelected: Story = {
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

function InteractiveTriggerSection() {
  const [value, setValue] = useState<TriggerFormState>({ type: 'webhook', auth: 'hmac' });
  return <TriggerSection value={value} onChange={setValue} policyId="example-policy" />;
}

export const Interactive: Story = {
  render: () => <InteractiveTriggerSection />,
};

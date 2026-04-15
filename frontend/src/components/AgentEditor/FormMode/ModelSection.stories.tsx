import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import '@/tokens.css';
import { ModelSection } from './ModelSection';
import type { ModelFormState } from './types';
import decoratorStyles from './ModelSection.stories.module.css';

const MOCK_MODELS_RESPONSE = JSON.stringify({
  data: [
    {
      provider: 'anthropic',
      models: [
        { name: 'claude-opus-4-6', display_name: 'Claude Opus 4.6' },
        { name: 'claude-sonnet-4-6', display_name: 'Claude Sonnet 4.6' },
      ],
    },
    {
      provider: 'google',
      models: [
        { name: 'gemini-2.5-pro', display_name: 'Gemini 2.5 Pro' },
        { name: 'gemini-2.0-flash', display_name: 'Gemini 2.0 Flash' },
      ],
    },
  ],
});

function makeQueryClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
}

const MOCK_NO_MODELS_RESPONSE = JSON.stringify({ data: [] });

const meta: Meta<typeof ModelSection> = {
  title: 'PolicyEditor/FormMode/ModelSection',
  component: ModelSection,
  decorators: [
    (Story) => {
      // Fresh query client per story to avoid stale cache across stories.
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
      if (url.includes('/api/v1/models')) {
        return new Response(MOCK_MODELS_RESPONSE, {
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
type Story = StoryObj<typeof ModelSection>;

export const AnthropicSelected: Story = {
  args: {
    value: { provider: 'anthropic', model: 'claude-sonnet-4-6' },
    onChange: fn(),
  },
};

export const GoogleSelected: Story = {
  args: {
    value: { provider: 'google', model: 'gemini-2.0-flash' },
    onChange: fn(),
  },
};

function InteractiveModelSection() {
  const [value, setValue] = useState<ModelFormState>({ provider: 'anthropic', model: 'claude-sonnet-4-6' });
  return <ModelSection value={value} onChange={setValue} />;
}

export const Interactive: Story = {
  render: () => <InteractiveModelSection />,
};

// NoModels shows the disabled placeholder when the operator has not yet enabled
// any models (e.g. immediately after a fresh install before visiting Admin → Models).
export const NoModels: Story = {
  args: {
    value: { provider: '', model: '' },
    onChange: fn(),
  },
  beforeEach: () => {
    const originalFetch = window.fetch;
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string'
        ? input
        : input instanceof URL
          ? input.toString()
          : input.url;
      if (url.includes('/api/v1/models')) {
        return new Response(MOCK_NO_MODELS_RESPONSE, {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      return originalFetch(input, init);
    };
    return () => { window.fetch = originalFetch; };
  },
};

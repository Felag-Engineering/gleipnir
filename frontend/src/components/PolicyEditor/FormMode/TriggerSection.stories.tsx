import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import '@/tokens.css';
import { TriggerSection } from './TriggerSection';
import type { TriggerFormState } from './types';
import decoratorStyles from './TriggerSection.stories.module.css';

const meta: Meta<typeof TriggerSection> = {
  title: 'PolicyEditor/FormMode/TriggerSection',
  component: TriggerSection,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof TriggerSection>;

export const WebhookSelected: Story = {
  args: {
    value: { type: 'webhook' },
    policyId: 'abc-123',
    onChange: fn(),
  },
};

export const WebhookNewAgent: Story = {
  args: {
    value: { type: 'webhook' },
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
  const [value, setValue] = useState<TriggerFormState>({ type: 'webhook' });
  return <TriggerSection value={value} onChange={setValue} policyId="example-policy" />;
}

export const Interactive: Story = {
  render: () => <InteractiveTriggerSection />,
};

import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import '../../../../tokens.css';
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

export const WebhookNewPolicy: Story = {
  args: {
    value: { type: 'webhook' },
    onChange: fn(),
  },
};

export const CronSelected: Story = {
  args: {
    value: { type: 'cron', schedule: '0 * * * *' },
    policyId: 'hourly-check',
    onChange: fn(),
  },
};

export const PollSelected: Story = {
  args: {
    value: {
      type: 'poll',
      interval: '5m',
      request: {
        url: 'https://api.github.com/repos/owner/repo/issues',
        method: 'GET',
        headers: 'Authorization: Bearer ghp_token\nAccept: application/vnd.github+json',
        body: undefined,
      },
      filter: '.[] | select(.state == "open")',
    },
    policyId: 'poll-issues',
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

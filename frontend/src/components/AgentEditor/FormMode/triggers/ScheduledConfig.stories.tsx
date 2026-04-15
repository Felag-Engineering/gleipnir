import type { Meta, StoryObj } from '@storybook/react-vite';
import { useState } from 'react';
import '@/tokens.css';
import { ScheduledConfig } from './ScheduledConfig';
import type { TriggerFormState, ScheduledTriggerState } from '../types';
import decoratorStyles from '../TriggerSection.stories.module.css';

const meta: Meta<typeof ScheduledConfig> = {
  title: 'PolicyEditor/FormMode/triggers/ScheduledConfig',
  component: ScheduledConfig,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof ScheduledConfig>;

export const Empty: Story = {
  args: {
    value: { type: 'scheduled', fireAt: [] },
    onChange: () => {},
  },
};

export const WithEntries: Story = {
  args: {
    value: {
      type: 'scheduled',
      fireAt: ['2025-06-01T09:00:00Z', '2025-12-31T23:00:00Z'],
    },
    onChange: () => {},
  },
};

function InteractiveScheduledConfig() {
  const [value, setValue] = useState<ScheduledTriggerState>({ type: 'scheduled', fireAt: [] });
  return (
    <ScheduledConfig
      value={value}
      onChange={(next: TriggerFormState) => setValue(next as ScheduledTriggerState)}
    />
  );
}

export const Interactive: Story = {
  render: () => <InteractiveScheduledConfig />,
};

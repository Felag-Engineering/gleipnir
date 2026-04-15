import type { Meta, StoryObj } from '@storybook/react-vite';
import { useState } from 'react';
import '@/tokens.css';
import { PollConfig } from './PollConfig';
import type { TriggerFormState, PollTriggerState } from '../types';
import decoratorStyles from '../TriggerSection.stories.module.css';

const meta: Meta<typeof PollConfig> = {
  title: 'PolicyEditor/FormMode/triggers/PollConfig',
  component: PollConfig,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof PollConfig>;

export const SingleCheck: Story = {
  args: {
    value: {
      type: 'poll',
      interval: '5m',
      match: 'all',
      checks: [
        { tool: 'monitor.check_status', input: '', path: '$.status', comparator: 'equals', value: 'degraded' },
      ],
    },
    onChange: () => {},
  },
};

export const MultipleChecksAny: Story = {
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
    onChange: () => {},
  },
};

function InteractivePollConfig() {
  const [value, setValue] = useState<PollTriggerState>({
    type: 'poll',
    interval: '5m',
    match: 'all',
    checks: [{ tool: '', input: '', path: '', comparator: 'equals', value: '' }],
  });
  return (
    <PollConfig
      value={value}
      onChange={(next: TriggerFormState) => setValue(next as PollTriggerState)}
    />
  );
}

export const Interactive: Story = {
  render: () => <InteractivePollConfig />,
};

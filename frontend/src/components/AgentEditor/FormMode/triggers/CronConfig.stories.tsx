import type { Meta, StoryObj } from '@storybook/react-vite';
import { useState } from 'react';
import '@/tokens.css';
import { CronConfig } from './CronConfig';
import type { TriggerFormState, CronTriggerState } from '../types';
import decoratorStyles from '../TriggerSection.stories.module.css';

const meta: Meta<typeof CronConfig> = {
  title: 'PolicyEditor/FormMode/triggers/CronConfig',
  component: CronConfig,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof CronConfig>;

export const Empty: Story = {
  args: {
    value: { type: 'cron', cronExpr: '' },
    onChange: () => {},
  },
};

export const EveryMonday: Story = {
  args: {
    value: { type: 'cron', cronExpr: '0 9 * * 1' },
    onChange: () => {},
  },
};

export const EveryFifteenMinutes: Story = {
  args: {
    value: { type: 'cron', cronExpr: '*/15 * * * *' },
    onChange: () => {},
  },
};

export const Invalid: Story = {
  args: {
    value: { type: 'cron', cronExpr: 'not a cron' },
    onChange: () => {},
    errors: [
      {
        field: 'trigger.cron_expr',
        message: 'trigger.cron_expr invalid expression: expected exactly 5 fields, found 3: [not a cron]',
      },
    ],
  },
};

function InteractiveCronConfig() {
  const [value, setValue] = useState<CronTriggerState>({ type: 'cron', cronExpr: '' });
  return (
    <CronConfig
      value={value}
      onChange={(next: TriggerFormState) => setValue(next as CronTriggerState)}
    />
  );
}

export const Interactive: Story = {
  render: () => <InteractiveCronConfig />,
};

import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import '@/tokens.css';
import { RunLimitsSection } from './RunLimitsSection';
import type { RunLimitsFormState } from './types';
import decoratorStyles from './RunLimitsSection.stories.module.css';

const meta: Meta<typeof RunLimitsSection> = {
  title: 'PolicyEditor/FormMode/RunLimitsSection',
  component: RunLimitsSection,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof RunLimitsSection>;

export const Defaults: Story = {
  args: {
    value: { max_tokens_per_run: 100000, max_tool_calls_per_run: 50 },
    onChange: fn(),
  },
};

export const LowLimits: Story = {
  args: {
    value: { max_tokens_per_run: 10000, max_tool_calls_per_run: 5 },
    onChange: fn(),
  },
};

export const Unlimited: Story = {
  args: {
    value: { max_tokens_per_run: 0, max_tool_calls_per_run: 0 },
    onChange: fn(),
  },
};

function InteractiveRunLimitsSection() {
  const [value, setValue] = useState<RunLimitsFormState>({
    max_tokens_per_run: 100000,
    max_tool_calls_per_run: 50,
  });
  return <RunLimitsSection value={value} onChange={setValue} />;
}

export const Interactive: Story = {
  render: () => <InteractiveRunLimitsSection />,
};

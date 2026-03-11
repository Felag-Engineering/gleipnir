import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import '@/tokens.css';
import { ConcurrencySection } from './ConcurrencySection';
import type { ConcurrencyFormState } from './types';
import decoratorStyles from './ConcurrencySection.stories.module.css';

const meta: Meta<typeof ConcurrencySection> = {
  title: 'PolicyEditor/FormMode/ConcurrencySection',
  component: ConcurrencySection,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof ConcurrencySection>;

export const SkipSelected: Story = {
  args: {
    value: { concurrency: 'skip' },
    onChange: fn(),
  },
};

export const QueueSelected: Story = {
  args: {
    value: { concurrency: 'queue' },
    onChange: fn(),
  },
};

export const ParallelSelected: Story = {
  args: {
    value: { concurrency: 'parallel' },
    onChange: fn(),
  },
};

export const ReplaceSelected: Story = {
  args: {
    value: { concurrency: 'replace' },
    onChange: fn(),
  },
};

function InteractiveConcurrencySection() {
  const [value, setValue] = useState<ConcurrencyFormState>({ concurrency: 'skip' });
  return <ConcurrencySection value={value} onChange={setValue} />;
}

export const Interactive: Story = {
  render: () => <InteractiveConcurrencySection />,
};

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
    value: { concurrency: 'skip', queueDepth: 0 },
    onChange: fn(),
  },
};

export const QueueSelected: Story = {
  args: {
    value: { concurrency: 'queue', queueDepth: 0 },
    onChange: fn(),
  },
};

// Shows the queue-depth input rendered with a non-zero value.
export const QueueWithDepth: Story = {
  args: {
    value: { concurrency: 'queue', queueDepth: 10 },
    onChange: fn(),
  },
};

export const ParallelSelected: Story = {
  args: {
    value: { concurrency: 'parallel', queueDepth: 0 },
    onChange: fn(),
  },
};

export const ReplaceSelected: Story = {
  args: {
    value: { concurrency: 'replace', queueDepth: 0 },
    onChange: fn(),
  },
};

function InteractiveConcurrencySection() {
  const [value, setValue] = useState<ConcurrencyFormState>({ concurrency: 'skip', queueDepth: 0 });
  return <ConcurrencySection value={value} onChange={setValue} />;
}

export const Interactive: Story = {
  render: () => <InteractiveConcurrencySection />,
};

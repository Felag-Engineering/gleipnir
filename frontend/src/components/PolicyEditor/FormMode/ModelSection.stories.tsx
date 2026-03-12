import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import '@/tokens.css';
import { ModelSection } from './ModelSection';
import type { ModelFormState } from './types';
import decoratorStyles from './ModelSection.stories.module.css';

const meta: Meta<typeof ModelSection> = {
  title: 'PolicyEditor/FormMode/ModelSection',
  component: ModelSection,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof ModelSection>;

export const SonnetSelected: Story = {
  args: {
    value: { model: 'claude-sonnet-4-6' },
    onChange: fn(),
  },
};

export const OpusSelected: Story = {
  args: {
    value: { model: 'claude-opus-4-6' },
    onChange: fn(),
  },
};

export const HaikuSelected: Story = {
  args: {
    value: { model: 'claude-haiku-4-5-20251001' },
    onChange: fn(),
  },
};

function InteractiveModelSection() {
  const [value, setValue] = useState<ModelFormState>({ model: 'claude-sonnet-4-6' });
  return <ModelSection value={value} onChange={setValue} />;
}

export const Interactive: Story = {
  render: () => <InteractiveModelSection />,
};

import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import '@/tokens.css';
import { TaskInstructionsSection } from './TaskInstructionsSection';
import type { TaskInstructionsFormState } from './types';
import decoratorStyles from './TaskInstructionsSection.stories.module.css';

const meta: Meta<typeof TaskInstructionsSection> = {
  title: 'PolicyEditor/FormMode/TaskInstructionsSection',
  component: TaskInstructionsSection,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof TaskInstructionsSection>;

export const Empty: Story = {
  args: {
    value: { task: '' },
    onChange: fn(),
  },
};

export const WithContent: Story = {
  args: {
    value: {
      task: 'Check the GitHub repository for new open issues. For each issue labeled "bug", add a comment asking for reproduction steps if none are present.',
    },
    onChange: fn(),
  },
};

function InteractiveTaskInstructionsSection() {
  const [value, setValue] = useState<TaskInstructionsFormState>({ task: '' });
  return <TaskInstructionsSection value={value} onChange={setValue} />;
}

export const Interactive: Story = {
  render: () => <InteractiveTaskInstructionsSection />,
};

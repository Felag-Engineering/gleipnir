import type { Meta, StoryObj } from '@storybook/react-vite';
import { ReasoningTrace } from './ReasoningTrace';
import { SAMPLE_APPROVALS } from './fixtures';

const meta: Meta<typeof ReasoningTrace> = {
  title: 'Dashboard/ReasoningTrace',
  component: ReasoningTrace,
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 600, padding: 16, background: '#0f1219', borderRadius: 8 }}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof ReasoningTrace>;

export const ShortTrace: Story = {
  args: {
    steps: SAMPLE_APPROVALS[0].reasoning,
  },
};

export const LongTrace: Story = {
  args: {
    steps: SAMPLE_APPROVALS[1].reasoning,
  },
};

export const ThoughtOnly: Story = {
  args: {
    steps: [
      { type: 'thought', text: 'The trigger payload indicates task #1040 has had its last sub-task checked off. I should verify the task state before closing.' },
      { type: 'thought', text: 'All four sub-tasks complete, due date today, no blockers. Criteria met for closure.' },
    ],
  },
};

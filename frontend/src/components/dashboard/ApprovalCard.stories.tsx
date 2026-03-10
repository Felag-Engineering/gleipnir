import type { Meta, StoryObj } from '@storybook/react-vite';
import { ApprovalCard } from './ApprovalCard';
import { GLOBAL_STYLES } from './styles';
import { SAMPLE_APPROVALS } from './fixtures';

const meta: Meta<typeof ApprovalCard> = {
  title: 'Dashboard/ApprovalCard',
  component: ApprovalCard,
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 700, padding: 20 }}>
        <style>{GLOBAL_STYLES}</style>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof ApprovalCard>;

export const TaskClose: Story = {
  args: {
    def: SAMPLE_APPROVALS[0],
    onDecide: (id, decision, note) => console.log('Decision:', { id, decision, note }),
  },
};

export const IncidentCreation: Story = {
  args: {
    def: SAMPLE_APPROVALS[1],
    onDecide: (id, decision, note) => console.log('Decision:', { id, decision, note }),
  },
};

export const UrgentTimer: Story = {
  args: {
    def: {
      ...SAMPLE_APPROVALS[0],
      expiresAt: new Date(Date.now() + 2 * 60 * 1000).toISOString(), // 2 minutes left
    },
    onDecide: (id, decision, note) => console.log('Decision:', { id, decision, note }),
  },
};

export const MultiplePending: Story = {
  render: () => (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {SAMPLE_APPROVALS.map(def => (
        <ApprovalCard
          key={def.id}
          def={def}
          onDecide={(id, decision, note) => console.log('Decision:', { id, decision, note })}
        />
      ))}
    </div>
  ),
};

import type { Meta, StoryObj } from '@storybook/react-vite';
import { StatusBadge } from './StatusBadge';
import { GLOBAL_STYLES } from './styles';

const meta: Meta<typeof StatusBadge> = {
  title: 'Dashboard/StatusBadge',
  component: StatusBadge,
  decorators: [
    (Story) => (
      <>
        <style>{GLOBAL_STYLES}</style>
        <Story />
      </>
    ),
  ],
  argTypes: {
    status: {
      control: 'select',
      options: ['complete', 'running', 'waiting_for_approval', 'failed', 'interrupted'],
    },
  },
};

export default meta;
type Story = StoryObj<typeof StatusBadge>;

export const Complete: Story = { args: { status: 'complete' } };
export const Running: Story = { args: { status: 'running' } };
export const AwaitingApproval: Story = { args: { status: 'waiting_for_approval' } };
export const Failed: Story = { args: { status: 'failed' } };
export const Interrupted: Story = { args: { status: 'interrupted' } };

export const AllStates: Story = {
  render: () => (
    <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
      <StatusBadge status="complete" />
      <StatusBadge status="running" />
      <StatusBadge status="waiting_for_approval" />
      <StatusBadge status="failed" />
      <StatusBadge status="interrupted" />
    </div>
  ),
};

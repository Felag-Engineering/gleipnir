import type { Meta, StoryObj } from '@storybook/react-vite';
import '../../../tokens.css';
import { StatusBadge } from './StatusBadge';

const meta: Meta<typeof StatusBadge> = {
  title: 'Dashboard/StatusBadge',
  component: StatusBadge,
  argTypes: {
    status: {
      control: 'select',
      options: ['complete', 'running', 'waiting_for_approval', 'failed', 'interrupted', 'pending'],
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
export const Pending: Story = { args: { status: 'pending' } };

export const AllStates: Story = {
  render: () => (
    <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
      <StatusBadge status="complete" />
      <StatusBadge status="running" />
      <StatusBadge status="waiting_for_approval" />
      <StatusBadge status="failed" />
      <StatusBadge status="interrupted" />
      <StatusBadge status="pending" />
    </div>
  ),
};

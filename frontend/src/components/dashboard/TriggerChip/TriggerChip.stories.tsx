import type { Meta, StoryObj } from '@storybook/react-vite';
import '@/tokens.css';
import { TriggerChip } from './TriggerChip';

const meta: Meta<typeof TriggerChip> = {
  title: 'Dashboard/TriggerChip',
  component: TriggerChip,
  argTypes: {
    type: { control: 'select', options: ['webhook', 'cron', 'poll', 'manual', 'scheduled'] },
  },
};

export default meta;
type Story = StoryObj<typeof TriggerChip>;

export const Webhook: Story = { args: { type: 'webhook' } };
export const Cron: Story = { args: { type: 'cron' } };
export const Poll: Story = { args: { type: 'poll' } };
export const Manual: Story = { args: { type: 'manual' } };
export const Scheduled: Story = { args: { type: 'scheduled' } };
export const ScheduledPaused: Story = { args: { type: 'scheduled', pausedAt: '2026-06-01T09:00:00Z' } };

export const AllTypes: Story = {
  render: () => (
    <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
      <TriggerChip type="webhook" />
      <TriggerChip type="cron" />
      <TriggerChip type="poll" />
      <TriggerChip type="manual" />
      <TriggerChip type="scheduled" />
      <TriggerChip type="scheduled" pausedAt="2026-06-01T09:00:00Z" />
    </div>
  ),
};

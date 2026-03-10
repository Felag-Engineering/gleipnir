import type { Meta, StoryObj } from '@storybook/react-vite';
import { TriggerChip } from './TriggerChip';

const meta: Meta<typeof TriggerChip> = {
  title: 'Dashboard/TriggerChip',
  component: TriggerChip,
  argTypes: {
    type: { control: 'select', options: ['webhook', 'cron', 'poll'] },
  },
};

export default meta;
type Story = StoryObj<typeof TriggerChip>;

export const Webhook: Story = { args: { type: 'webhook' } };
export const Cron: Story = { args: { type: 'cron' } };
export const Poll: Story = { args: { type: 'poll' } };

export const AllTypes: Story = {
  render: () => (
    <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
      <TriggerChip type="webhook" />
      <TriggerChip type="cron" />
      <TriggerChip type="poll" />
    </div>
  ),
};

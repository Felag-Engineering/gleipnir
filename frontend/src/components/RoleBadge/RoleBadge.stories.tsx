import type { Meta, StoryObj } from '@storybook/react-vite';
import '@/tokens.css';
import { RoleBadge } from './RoleBadge';

const meta: Meta<typeof RoleBadge> = {
  title: 'Components/RoleBadge',
  component: RoleBadge,
  argTypes: {
    role: { control: 'select', options: ['tool', 'feedback'] },
  },
};

export default meta;
type Story = StoryObj<typeof RoleBadge>;

export const Tool: Story     = { args: { role: 'tool'     } };
export const Feedback: Story = { args: { role: 'feedback' } };

export const AllRoles: Story = {
  render: () => (
    <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
      <RoleBadge role="tool"     />
      <RoleBadge role="feedback" />
    </div>
  ),
};

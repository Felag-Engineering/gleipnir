import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { RoleChips } from './RoleChips'

const meta: Meta<typeof RoleChips> = {
  title: 'UsersPage/RoleChips',
  component: RoleChips,
  args: {
    userId: 'u1',
    onToggle: () => {},
    disabled: false,
  },
}

export default meta
type Story = StoryObj<typeof RoleChips>

export const AllActive: Story = {
  args: { roles: ['admin', 'operator', 'approver', 'auditor'] },
}

export const PartialRoles: Story = {
  args: { roles: ['operator'] },
}

export const NoRoles: Story = {
  args: { roles: [] },
}

export const Disabled: Story = {
  args: { roles: ['admin'], disabled: true },
}

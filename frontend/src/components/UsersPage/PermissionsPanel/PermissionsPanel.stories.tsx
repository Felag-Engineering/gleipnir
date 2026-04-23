import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { PermissionsPanel } from './PermissionsPanel'

const meta: Meta<typeof PermissionsPanel> = {
  title: 'UsersPage/PermissionsPanel',
  component: PermissionsPanel,
}

export default meta
type Story = StoryObj<typeof PermissionsPanel>

export const Admin: Story = {
  args: { role: 'admin' },
}

export const Operator: Story = {
  args: { role: 'operator' },
}

export const Approver: Story = {
  args: { role: 'approver' },
}

export const Auditor: Story = {
  args: { role: 'auditor' },
}

export const Empty: Story = {
  args: { role: null },
}

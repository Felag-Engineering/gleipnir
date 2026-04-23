import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { CreateUserModal } from './CreateUserModal'

const meta: Meta<typeof CreateUserModal> = {
  title: 'UsersPage/CreateUserModal',
  component: CreateUserModal,
}

export default meta
type Story = StoryObj<typeof CreateUserModal>

export const Idle: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: false, error: null },
}

export const Pending: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: true, error: null },
}

export const WithError: Story = {
  args: {
    onClose: () => {},
    onSubmit: () => {},
    isPending: false,
    error: { message: 'Username already exists' } as never,
  },
}

// Shows the two-column layout with the permissions panel populated.
// Interact with the role checkboxes to preview panel updates.
export const WithRolesPreselected: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: false, error: null },
}

import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ClearCredentialsModal } from './ClearCredentialsModal'

const meta: Meta<typeof ClearCredentialsModal> = {
  title: 'Admin/ClearCredentialsModal',
  component: ClearCredentialsModal,
}

export default meta
type Story = StoryObj<typeof ClearCredentialsModal>

// Default — idle state, no error. Admin sees the consequence copy and can confirm
// or cancel.
export const Default: Story = {
  args: {
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: null,
  },
}

// Pending — the clear request is in flight; the button is disabled and shows
// loading text.
export const Pending: Story = {
  args: {
    onClose: () => {},
    onConfirm: () => {},
    isPending: true,
    error: null,
  },
}

// Error — the DELETE request failed; the error detail is shown under the modal body.
export const ErrorState: Story = {
  args: {
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: 'Clear failed. Please try again.',
  },
}

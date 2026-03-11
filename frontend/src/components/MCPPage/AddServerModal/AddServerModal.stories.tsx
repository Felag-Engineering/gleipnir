import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { AddServerModal } from './AddServerModal'

const meta: Meta<typeof AddServerModal> = {
  title: 'MCPPage/AddServerModal',
  component: AddServerModal,
}

export default meta
type Story = StoryObj<typeof AddServerModal>

export const Idle: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: false, error: null },
}

export const Pending: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: true, error: null },
}

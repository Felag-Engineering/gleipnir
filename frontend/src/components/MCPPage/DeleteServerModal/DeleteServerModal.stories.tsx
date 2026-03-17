import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { DeleteServerModal } from './DeleteServerModal'

const meta: Meta<typeof DeleteServerModal> = {
  title: 'ToolsPage/DeleteServerModal',
  component: DeleteServerModal,
}

export default meta
type Story = StoryObj<typeof DeleteServerModal>

export const Confirm: Story = {
  args: {
    serverName: 'kubectl-mcp',
    toolCount: 5,
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: null,
  },
}

export const Pending: Story = {
  args: {
    serverName: 'kubectl-mcp',
    toolCount: 5,
    onClose: () => {},
    onConfirm: () => {},
    isPending: true,
    error: null,
  },
}

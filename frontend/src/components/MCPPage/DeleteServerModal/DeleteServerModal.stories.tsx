import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ApiError } from '@/api/fetch'
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

// After a 409 the submit button switches to "Force delete" so the operator
// can override the in-use check after seeing the affected policies.
export const InUseConflict: Story = {
  args: {
    serverName: 'kubectl-mcp',
    toolCount: 5,
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: new ApiError(
      409,
      'MCP server is referenced by active policies',
      'policies referencing this server: deploy-bot, smoke-tests — pass ?force=true to delete anyway',
    ),
  },
}

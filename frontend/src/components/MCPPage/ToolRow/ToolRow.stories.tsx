import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ToolRow } from './ToolRow'
import type { ApiMcpTool } from '@/api/types'

const meta: Meta<typeof ToolRow> = {
  title: 'ToolsPage/ToolRow',
  component: ToolRow,
}

export default meta
type Story = StoryObj<typeof ToolRow>

const tool: ApiMcpTool = {
  id: 't1',
  server_id: 'srv1',
  name: 'kubectl.get_pods',
  description: 'List pods across namespaces with status and restart counts.',
  input_schema: { namespace: { type: 'string', required: true } },
}

export const Default: Story = {
  args: { tool },
}

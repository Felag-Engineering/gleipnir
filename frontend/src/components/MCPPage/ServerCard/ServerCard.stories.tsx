import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ServerCard } from './ServerCard'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'

const meta: Meta<typeof ServerCard> = {
  title: 'ToolsPage/ServerCard',
  component: ServerCard,
}

export default meta
type Story = StoryObj<typeof ServerCard>

const server: ApiMcpServer = {
  id: 'srv1',
  name: 'kubectl-mcp',
  url: 'http://kubectl-mcp:8080',
  last_discovered_at: new Date(Date.now() - 3_600_000).toISOString(),
  has_drift: false,
  created_at: new Date(Date.now() - 86_400_000).toISOString(),
}

const tools: ApiMcpTool[] = [
  { id: 't1', server_id: 'srv1', name: 'kubectl.get_pods', description: 'List pods.', capability_role: 'sensor', input_schema: {} },
  { id: 't2', server_id: 'srv1', name: 'kubectl.delete_pod', description: 'Delete a pod.', capability_role: 'actuator', input_schema: {} },
]

export const Connected: Story = {
  args: {
    server,
    tools,
    toolsLoading: false,
    isDiscovering: false,
    onDiscover: () => {},
    onDelete: () => {},
    onRoleChange: () => {},
    updatingToolId: null,
  },
}

export const Discovering: Story = {
  args: {
    ...Connected.args,
    isDiscovering: true,
  },
}

export const Unreachable: Story = {
  args: {
    ...Connected.args,
    server: { ...server, last_discovered_at: null },
  },
}

export const Drifted: Story = {
  args: {
    ...Connected.args,
    server: { ...server, has_drift: true },
  },
}

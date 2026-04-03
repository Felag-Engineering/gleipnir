import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ServerDetailModal } from './ServerDetailModal'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'

const server: ApiMcpServer = {
  id: 'srv-1',
  name: 'test-server',
  url: 'http://mcp-test-server:8090/mcp',
  last_discovered_at: new Date(Date.now() - 3_600_000 * 4).toISOString(),
  has_drift: false,
  created_at: new Date(Date.now() - 86_400_000).toISOString(),
}

const tools: ApiMcpTool[] = [
  {
    id: 't1', server_id: 'srv-1', name: 'echo',
    description: 'Echo the provided message back unchanged.',
    input_schema: { properties: { message: { type: 'string' } }, required: ['message'], type: 'object' },
  },
  {
    id: 't2', server_id: 'srv-1', name: 'get_current_time',
    description: 'Return the current UTC time as an ISO 8601 string.',
    input_schema: { properties: {}, type: 'object' },
  },
  {
    id: 't3', server_id: 'srv-1', name: 'send_notification',
    description: 'Simulate sending a notification to a channel.',
    input_schema: {
      properties: { channel: { type: 'string' }, message: { type: 'string' } },
      required: ['channel', 'message'], type: 'object',
    },
  },
]

const meta: Meta<typeof ServerDetailModal> = {
  title: 'ToolsPage/ServerDetailModal',
  component: ServerDetailModal,
  parameters: { layout: 'fullscreen' },
}

export default meta
type Story = StoryObj<typeof ServerDetailModal>

export const Healthy: Story = {
  args: {
    server, tools, toolsLoading: false, isDiscovering: false,
    policies: [
      { id: 'p1', name: 'system-health-check', trigger_type: 'webhook', folder: 'testing',
        model: '', tool_count: 3, tool_refs: ['test-server.echo', 'test-server.get_current_time', 'test-server.get_system_status'],
        avg_token_cost: 0, created_at: '', updated_at: '', paused_at: null, latest_run: null },
    ],
    onClose: () => {}, onDiscover: () => {}, onDelete: () => {},
  },
}

export const WithDrift: Story = {
  args: { ...Healthy.args, server: { ...server, has_drift: true } },
}

export const Unreachable: Story = {
  args: { ...Healthy.args, server: { ...server, last_discovered_at: null }, tools: [] },
}

export const Discovering: Story = {
  args: { ...Healthy.args, isDiscovering: true },
}

export const Loading: Story = {
  args: { ...Healthy.args, toolsLoading: true, tools: undefined },
}

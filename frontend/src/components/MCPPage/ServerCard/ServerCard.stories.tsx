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
  name: 'test-server',
  url: 'http://mcp-test-server:8090/mcp',
  last_discovered_at: new Date(Date.now() - 3_600_000 * 4).toISOString(),
  has_drift: false,
  created_at: new Date(Date.now() - 86_400_000).toISOString(),
  is_arcade_gateway: false,
}

const tools: ApiMcpTool[] = [
  { id: 't1', server_id: 'srv1', name: 'echo', description: 'Echo message.', input_schema: {}, enabled: true },
  { id: 't2', server_id: 'srv1', name: 'get_current_time', description: 'Get time.', input_schema: {}, enabled: true },
  { id: 't3', server_id: 'srv1', name: 'get_system_status', description: 'Get status.', input_schema: {}, enabled: true },
  { id: 't4', server_id: 'srv1', name: 'list_items', description: 'List items.', input_schema: {}, enabled: true },
  { id: 't5', server_id: 'srv1', name: 'send_notification', description: 'Send notification.', input_schema: {}, enabled: true },
  { id: 't6', server_id: 'srv1', name: 'update_item_stock', description: 'Update stock.', input_schema: {}, enabled: true },
  { id: 't7', server_id: 'srv1', name: 'write_file', description: 'Write file.', input_schema: {}, enabled: true },
]

export const Healthy: Story = {
  args: { server, tools, toolsLoading: false, isDiscovering: false, onClick: () => {} },
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

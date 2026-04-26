import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
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
  is_arcade_gateway: false,
}

const tools: ApiMcpTool[] = [
  {
    id: 't1', server_id: 'srv-1', name: 'echo',
    description: 'Echo the provided message back unchanged.',
    input_schema: { properties: { message: { type: 'string' } }, required: ['message'], type: 'object' },
    enabled: true,
  },
  {
    id: 't2', server_id: 'srv-1', name: 'get_current_time',
    description: 'Return the current UTC time as an ISO 8601 string.',
    input_schema: { properties: {}, type: 'object' },
    enabled: true,
  },
  {
    id: 't3', server_id: 'srv-1', name: 'send_notification',
    description: 'Simulate sending a notification to a channel.',
    input_schema: {
      properties: { channel: { type: 'string' }, message: { type: 'string' } },
      required: ['channel', 'message'], type: 'object',
    },
    enabled: true,
  },
]

const meta: Meta<typeof ServerDetailModal> = {
  title: 'ToolsPage/ServerDetailModal',
  component: ServerDetailModal,
  parameters: {
    layout: 'fullscreen',
    msw: {
      handlers: [
        http.post('/api/v1/mcp/servers/:id/arcade/authorize', () =>
          HttpResponse.json({ data: { status: 'completed' } }),
        ),
      ],
    },
  },
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof ServerDetailModal>

export const Healthy: Story = {
  args: {
    server, tools, toolsLoading: false, isDiscovering: false, canManage: true,
    policies: [
      { id: 'p1', name: 'system-health-check', trigger_type: 'webhook', folder: 'testing',
        model: '', tool_count: 3, tool_refs: ['test-server.echo', 'test-server.get_current_time', 'test-server.get_system_status'],
        avg_token_cost: 0, run_count: 0, created_at: '', updated_at: '', paused_at: null, latest_run: null, next_fire_at: null },
    ],
    onClose: () => {}, onDiscover: () => {}, onDelete: () => {},
  },
}

export const ReadOnly: Story = {
  args: { ...Healthy.args, canManage: false },
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

export const WithDisabledTool: Story = {
  args: {
    ...Healthy.args,
    tools: [
      ...tools.slice(0, 2),
      { ...tools[2], enabled: false },
    ],
  },
}

export const ArcadeGateway: Story = {
  args: {
    ...Healthy.args,
    server: {
      ...server,
      is_arcade_gateway: true,
      url: 'https://api.arcade.dev/mcp/test',
      auth_header_keys: ['Authorization', 'Arcade-User-ID'],
    },
    tools: [
      { id: 't4', server_id: 'srv-1', name: 'Gmail.SendEmail', description: 'Send email.', input_schema: {}, enabled: true },
      { id: 't5', server_id: 'srv-1', name: 'Gmail.ListEmails', description: 'List emails.', input_schema: {}, enabled: true },
      { id: 't6', server_id: 'srv-1', name: 'GoogleCalendar.CreateEvent', description: 'Create event.', input_schema: {}, enabled: true },
    ],
  },
}

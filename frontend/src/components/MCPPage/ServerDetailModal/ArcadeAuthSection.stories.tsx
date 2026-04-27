import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { userEvent, within } from 'storybook/test'
import '@/tokens.css'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { ArcadeAuthSection } from './ArcadeAuthSection'

const server: ApiMcpServer = {
  id: 'srv-1',
  name: 'arcade-gateway',
  url: 'https://api.arcade.dev/mcp/test',
  last_discovered_at: new Date(Date.now() - 3_600_000).toISOString(),
  has_drift: false,
  created_at: new Date(Date.now() - 86_400_000).toISOString(),
  is_arcade_gateway: true,
  auth_header_keys: ['Authorization', 'Arcade-User-ID'],
}

const tools: ApiMcpTool[] = [
  {
    id: 't1',
    server_id: 'srv-1',
    name: 'Gmail.SendEmail',
    description: 'Send an email via Gmail.',
    input_schema: {},
    enabled: true,
  },
  {
    id: 't2',
    server_id: 'srv-1',
    name: 'Gmail.ListEmails',
    description: 'List emails in the inbox.',
    input_schema: {},
    enabled: true,
  },
  {
    id: 't3',
    server_id: 'srv-1',
    name: 'GoogleCalendar.CreateEvent',
    description: 'Create a calendar event.',
    input_schema: {},
    enabled: true,
  },
  {
    id: 't4',
    server_id: 'srv-1',
    name: 'GoogleCalendar.ListEvents',
    description: 'List upcoming calendar events.',
    input_schema: {},
    enabled: true,
  },
  {
    id: 't5',
    server_id: 'srv-1',
    name: 'Slack.SendMessage',
    description: 'Send a Slack message.',
    input_schema: {},
    enabled: true,
  },
]

function makeClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

const meta: Meta<typeof ArcadeAuthSection> = {
  title: 'ToolsPage/ArcadeAuthSection',
  component: ArcadeAuthSection,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeClient()}>
        <div style={{ maxWidth: 640, padding: 16 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof ArcadeAuthSection>

// Unknown state — all toolkits show "Check →" button (initial render, no auth status yet).
export const Unknown: Story = {
  args: { server, tools, canManage: true },
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/mcp/servers/:id/arcade/authorize', () =>
          HttpResponse.json({ data: { status: 'completed' } }),
        ),
      ],
    },
  },
}

// Authorized state — MSW returns completed immediately; the play function clicks
// the first toolkit's Check button so the component drives itself to authorized.
export const Authorized: Story = {
  args: { server, tools, canManage: true },
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/mcp/servers/:id/arcade/authorize', () =>
          HttpResponse.json({ data: { status: 'completed' } }),
        ),
      ],
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const btn = canvas.getAllByRole('button', { name: /check/i })[0]
    await userEvent.click(btn)
  },
}

export const ActionNeeded: Story = {
  args: { server, tools, canManage: true },
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/mcp/servers/:id/arcade/authorize', () =>
          HttpResponse.json({
            data: { status: 'pending', url: 'https://accounts.google.com/oauth', auth_id: 'auth-123' },
          }),
        ),
        http.post('/api/v1/mcp/servers/:id/arcade/authorize/wait', () =>
          HttpResponse.json({ data: { status: 'completed' } }),
        ),
      ],
    },
  },
}

// ReadOnly — canManage=false renders toolkit names and tool counts without buttons.
export const ReadOnly: Story = {
  args: { server, tools, canManage: false },
}

import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { useMcpServers, useMcpTools } from './queries/servers'
import { queryKeys } from './queryKeys'

const FIXTURE_SERVERS: ApiMcpServer[] = [
  {
    id: 'srv-1',
    name: 'Filesystem Tools',
    url: 'http://mcp-filesystem:8080',
    last_discovered_at: '2026-03-10T12:00:00Z',
    has_drift: false,
    created_at: '2026-03-01T00:00:00Z',
  },
  {
    id: 'srv-2',
    name: 'GitHub Tools',
    url: 'http://mcp-github:8080',
    last_discovered_at: null,
    has_drift: false,
    created_at: '2026-03-05T00:00:00Z',
  },
]

const FIXTURE_TOOLS: ApiMcpTool[] = [
  {
    id: 'tool-1',
    server_id: 'srv-1',
    name: 'read_file',
    description: 'Read the contents of a file at the given path',
    input_schema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
    enabled: true,
  },
  {
    id: 'tool-2',
    server_id: 'srv-1',
    name: 'write_file',
    description: 'Write content to a file at the given path',
    input_schema: {
      type: 'object',
      properties: { path: { type: 'string' }, content: { type: 'string' } },
      required: ['path', 'content'],
    },
    enabled: true,
  },
  {
    id: 'tool-3',
    server_id: 'srv-1',
    name: 'request_feedback',
    description: 'Send a message to the operator and wait for a response',
    input_schema: { type: 'object', properties: { message: { type: 'string' } }, required: ['message'] },
    enabled: true,
  },
]

function UseMcpServersDisplay() {
  const { data, status, error } = useMcpServers()
  return (
    <div style={{ fontFamily: 'IBM Plex Mono, monospace', fontSize: 13, color: '#E2E8F0', padding: 16 }}>
      <div>status: {status}</div>
      {data && (
        <pre style={{ marginTop: 8, color: '#94A3B8' }}>{JSON.stringify(data, null, 2)}</pre>
      )}
      {error && <div style={{ color: '#F87171' }}>error: {String(error)}</div>}
    </div>
  )
}

function UseMcpToolsDisplay({ serverId }: { serverId: string }) {
  const { data, status, error } = useMcpTools(serverId)
  return (
    <div style={{ fontFamily: 'IBM Plex Mono, monospace', fontSize: 13, color: '#E2E8F0', padding: 16 }}>
      <div>serverId: {serverId} — status: {status}</div>
      {data && (
        <pre style={{ marginTop: 8, color: '#94A3B8' }}>{JSON.stringify(data, null, 2)}</pre>
      )}
      {error && <div style={{ color: '#F87171' }}>error: {String(error)}</div>}
    </div>
  )
}

function makeQueryClient(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS)
  qc.setQueryData(queryKeys.servers.tools('srv-1'), FIXTURE_TOOLS)
  return qc
}

const serversMeta: Meta<typeof UseMcpServersDisplay> = {
  title: 'Hooks/useMcpServers',
  component: UseMcpServersDisplay,
}

export default serversMeta

export const Loaded: StoryObj<typeof UseMcpServersDisplay> = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const ToolsForServer: StoryObj<typeof UseMcpToolsDisplay> = {
  render: () => <UseMcpToolsDisplay serverId="srv-1" />,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

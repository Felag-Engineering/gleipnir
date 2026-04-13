import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { AddServerModal } from './AddServerModal'

function makeQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

const meta: Meta<typeof AddServerModal> = {
  title: 'ToolsPage/AddServerModal',
  component: AddServerModal,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof AddServerModal>

export const Idle: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: false, error: null },
}

export const Pending: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: true, error: null },
}

// Test connection result: successful, tools found.
export const TestConnectionSuccess: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: false, error: null },
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/mcp/servers/test', () =>
          HttpResponse.json({
            data: { ok: true, tool_count: 3, tools: ['read', 'write', 'list'], error: '' },
          }),
        ),
      ],
    },
  },
}

// Test connection result: server reachable but no tools registered.
export const TestConnectionNoTools: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: false, error: null },
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/mcp/servers/test', () =>
          HttpResponse.json({
            data: { ok: true, tool_count: 0, tools: [], error: '' },
          }),
        ),
      ],
    },
  },
}

// Test connection result: server unreachable or returned an error.
export const TestConnectionError: Story = {
  args: { onClose: () => {}, onSubmit: () => {}, isPending: false, error: null },
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/mcp/servers/test', () =>
          HttpResponse.json({
            data: { ok: false, tool_count: 0, tools: [], error: 'connection refused' },
          }),
        ),
      ],
    },
  },
}

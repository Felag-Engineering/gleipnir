import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import AdminSystemPage from './AdminSystemPage'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiSystemInfo } from '@/api/types'

const FIXTURE_SYSTEM_INFO: ApiSystemInfo = {
  version: 'v0.4.0',
  uptime: '3d 14h 22m',
  db_size: '4.2 MB',
  mcp_servers: 3,
  policies: 8,
  users: 2,
}

function makeQueryClient(opts: {
  systemInfo?: ApiSystemInfo
  maxTokens?: string
  maxToolCalls?: string
  publicUrl?: string
}): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.admin.settings, {
    max_tokens_per_run: opts.maxTokens ?? '0',
    max_tool_calls_per_run: opts.maxToolCalls ?? '0',
    public_url: opts.publicUrl ?? '',
  })
  if (opts.systemInfo) qc.setQueryData(queryKeys.admin.systemInfo, opts.systemInfo)
  return qc
}

const meta: Meta<typeof AdminSystemPage> = {
  title: 'Admin/SystemPage',
  component: AdminSystemPage,
}

export default meta
type Story = StoryObj<typeof AdminSystemPage>

export const Default: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider
        client={makeQueryClient({
          systemInfo: FIXTURE_SYSTEM_INFO,
          maxTokens: '100000',
          maxToolCalls: '50',
        })}
      >
        <div style={{ maxWidth: 720 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

export const WithPublicURL: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider
        client={makeQueryClient({
          systemInfo: FIXTURE_SYSTEM_INFO,
          maxTokens: '100000',
          maxToolCalls: '50',
          publicUrl: 'https://gleipnir.example.com',
        })}
      >
        <div style={{ maxWidth: 720 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

export const Unlimited: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider
        client={makeQueryClient({
          systemInfo: FIXTURE_SYSTEM_INFO,
          maxTokens: '0',
          maxToolCalls: '0',
        })}
      >
        <div style={{ maxWidth: 720 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

export const Loading: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        <div style={{ maxWidth: 720 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

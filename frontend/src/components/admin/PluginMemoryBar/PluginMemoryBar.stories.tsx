import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { PluginMemoryBar } from './PluginMemoryBar'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiPluginRSS } from '@/api/types'

const RSS_WITH_INSTANCES: ApiPluginRSS = {
  total_bytes: 431915008, // ≈ 412 MB
  instance_count: 3,
  instances: [
    {
      instance_id: 'inst-slack-prod',
      instance_name: 'slack-prod',
      plugin_id: 'plugin-slack',
      rss_bytes: 209715200, // 200 MB
      sampled_at: new Date().toISOString(),
    },
    {
      instance_id: 'inst-jira-prod',
      instance_name: 'jira-prod',
      plugin_id: 'plugin-jira',
      rss_bytes: 157286400, // 150 MB
      sampled_at: new Date().toISOString(),
    },
    {
      instance_id: 'inst-github-prod',
      instance_name: 'github-prod',
      plugin_id: 'plugin-github',
      rss_bytes: 64913408, // ≈ 61.9 MB
      sampled_at: new Date().toISOString(),
    },
  ],
}

const RSS_EMPTY: ApiPluginRSS = {
  total_bytes: 0,
  instance_count: 0,
  instances: [],
}

function makeQueryClient(rss: ApiPluginRSS): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.plugins.rss(), rss)
  return qc
}

const meta: Meta<typeof PluginMemoryBar> = {
  title: 'Admin/PluginMemoryBar',
  component: PluginMemoryBar,
  parameters: {
    layout: 'padded',
  },
}

export default meta
type Story = StoryObj<typeof PluginMemoryBar>

// Three running instances — shows the summary line with formatted bytes and count.
// Click the summary to expand the per-instance breakdown table.
export const WithInstances: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/plugins/rss', () =>
          HttpResponse.json({ data: RSS_WITH_INSTANCES }),
        ),
      ],
    },
  },
  render: () => (
    <QueryClientProvider client={makeQueryClient(RSS_WITH_INSTANCES)}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '16px' }}>
        <PluginMemoryBar />
      </div>
    </QueryClientProvider>
  ),
}

// No running instances — the component renders nothing (no memory bar shown).
export const NoRunningInstances: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/plugins/rss', () =>
          HttpResponse.json({ data: RSS_EMPTY }),
        ),
      ],
    },
  },
  render: () => (
    <QueryClientProvider client={makeQueryClient(RSS_EMPTY)}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '16px' }}>
        <PluginMemoryBar />
        <span style={{ color: 'gray', fontSize: '13px' }}>(nothing rendered — 0 instances)</span>
      </div>
    </QueryClientProvider>
  ),
}

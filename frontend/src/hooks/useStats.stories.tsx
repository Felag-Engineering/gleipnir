import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import type { ApiStats } from '../api/types'
import { useStats } from './useStats'
import { queryKeys } from './queryKeys'

const FIXTURE_STATS: ApiStats = {
  active_runs: 2,
  pending_approvals: 1,
  policy_count: 5,
  tokens_last_24h: 23680,
}

const FIXTURE_STATS_IDLE: ApiStats = {
  active_runs: 0,
  pending_approvals: 0,
  policy_count: 3,
  tokens_last_24h: 0,
}

function UseStatsDisplay() {
  const { data, status, error } = useStats()
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

function makeQueryClient(stats?: ApiStats): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  if (stats) {
    qc.setQueryData(queryKeys.stats.all, stats)
  }
  return qc
}

const meta: Meta<typeof UseStatsDisplay> = {
  title: 'Hooks/useStats',
  component: UseStatsDisplay,
}

export default meta
type Story = StoryObj<typeof UseStatsDisplay>

export const ActiveDashboard: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(FIXTURE_STATS)}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const IdleDashboard: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(FIXTURE_STATS_IDLE)}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const Loading: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

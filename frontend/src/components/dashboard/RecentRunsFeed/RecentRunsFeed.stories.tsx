import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { RecentRunsFeed } from './RecentRunsFeed'
import type { ApiRun, ApiRunsResponse } from '@/api/types'
import storyStyles from '../dashboard-stories.module.css'

const meta: Meta<typeof RecentRunsFeed> = {
  title: 'Dashboard/RecentRunsFeed',
  component: RecentRunsFeed,
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <div className={storyStyles.storyWrapperWide}>
            <Story />
          </div>
        </MemoryRouter>
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof RecentRunsFeed>

const MOCK_RUNS: ApiRun[] = [
  {
    id: 'r1',
    policy_id: 'p1',
    policy_name: 'deploy-staging',
    status: 'waiting_for_approval',
    trigger_type: 'webhook',
    started_at: '2026-04-02T11:50:00Z',
    completed_at: null,
    token_cost: 2100,
    error: null,
    created_at: '2026-04-02T11:50:00Z',
    system_prompt: null,
    model: 'claude-sonnet-4-6',
  },
  {
    id: 'r2',
    policy_id: 'p2',
    policy_name: 'log-anomalies',
    status: 'running',
    trigger_type: 'scheduled',
    started_at: '2026-04-02T11:48:00Z',
    completed_at: null,
    token_cost: 890,
    error: null,
    created_at: '2026-04-02T11:48:00Z',
    system_prompt: null,
    model: 'claude-haiku-3-5-20241022',
  },
  {
    id: 'r3',
    policy_id: 'p3',
    policy_name: 'sync-github',
    status: 'complete',
    trigger_type: 'webhook',
    started_at: '2026-04-02T11:40:00Z',
    completed_at: '2026-04-02T11:41:15Z',
    token_cost: 1400,
    error: null,
    created_at: '2026-04-02T11:40:00Z',
    system_prompt: null,
    model: 'claude-sonnet-4-6',
  },
  {
    id: 'r4',
    policy_id: 'p4',
    policy_name: 'backup-db',
    status: 'failed',
    trigger_type: 'scheduled',
    started_at: '2026-04-02T10:00:00Z',
    completed_at: '2026-04-02T10:01:30Z',
    token_cost: 300,
    error: 'tool call timed out',
    created_at: '2026-04-02T10:00:00Z',
    system_prompt: null,
    model: 'claude-sonnet-4-6',
  },
]

const MOCK_RESPONSE: ApiRunsResponse = { runs: MOCK_RUNS, total: MOCK_RUNS.length }

export const Default: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/runs', () => HttpResponse.json({ data: MOCK_RESPONSE })),
      ],
    },
  },
}

export const Empty: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/runs', () => HttpResponse.json({ data: { runs: [], total: 0 } })),
      ],
    },
  },
}

export const Loading: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/runs', async () => {
          await new Promise(r => setTimeout(r, 60_000))
          return HttpResponse.json({ data: MOCK_RESPONSE })
        }),
      ],
    },
  },
}

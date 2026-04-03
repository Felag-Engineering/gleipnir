import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import DashboardPage from './DashboardPage'
import type { ApiStats, ApiTimeSeriesResponse, ApiAttentionResponse, ApiRunsResponse } from '@/api/types'

// recharts uses ResizeObserver and getBoundingClientRect which are not available in jsdom.
// Replace chart components with simple wrappers that render their children so the
// surrounding page structure (titles, labels) is still testable.
vi.mock('recharts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('recharts')>()
  const Passthrough = ({ children }: { children?: React.ReactNode }) => <div>{children}</div>
  return {
    ...actual,
    ResponsiveContainer: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
    AreaChart: Passthrough,
    BarChart: Passthrough,
    LineChart: Passthrough,
    Area: () => null,
    Bar: () => null,
    Line: () => null,
    XAxis: () => null,
    YAxis: () => null,
    CartesianGrid: () => null,
    Tooltip: () => null,
    Legend: () => null,
  }
})

const STATS: ApiStats = {
  active_runs: 1,
  pending_approvals: 0,
  pending_feedback: 0,
  policy_count: 1,
  tokens_last_24h: 1000,
}

const TIMESERIES: ApiTimeSeriesResponse = {
  buckets: [],
}

const ATTENTION: ApiAttentionResponse = {
  items: [],
}

const RUNS: ApiRunsResponse = {
  runs: [],
  total: 0,
}

function makeClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderDashboard(queryClient: QueryClient) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <DashboardPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function setupDefaultHandlers() {
  server.use(
    http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
    http.get('/api/v1/stats/timeseries', () => HttpResponse.json({ data: TIMESERIES })),
    http.get('/api/v1/attention', () => HttpResponse.json({ data: ATTENTION })),
    http.get('/api/v1/runs', () => HttpResponse.json({ data: RUNS })),
    http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: [] })),
  )
}

describe('DashboardPage', () => {
  it('renders all dashboard sections', async () => {
    setupDefaultHandlers()
    renderDashboard(makeClient())

    await waitFor(() => {
      expect(screen.getByText('Control Center')).toBeInTheDocument()
    })
    expect(screen.getByText('RUN ACTIVITY')).toBeInTheDocument()
    expect(screen.getByText('COST BY MODEL')).toBeInTheDocument()
    expect(screen.getByText('RECENT RUNS')).toBeInTheDocument()
  })

  it('does not render the attention queue when there are no items', async () => {
    setupDefaultHandlers()
    renderDashboard(makeClient())

    // Give data time to load
    await waitFor(() => {
      expect(screen.getByText('RECENT RUNS')).toBeInTheDocument()
    })

    // NEEDS ATTENTION section should not be visible with zero items
    expect(screen.queryByText('NEEDS ATTENTION')).not.toBeInTheDocument()
  })

  it('renders the attention queue when there are items', async () => {
    server.use(
      http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
      http.get('/api/v1/stats/timeseries', () => HttpResponse.json({ data: TIMESERIES })),
      http.get('/api/v1/attention', () =>
        HttpResponse.json({
          data: {
            items: [
              {
                type: 'failure',
                request_id: '',
                run_id: 'r1',
                policy_id: 'p1',
                policy_name: 'test-policy',
                tool_name: '',
                message: 'tool timed out',
                expires_at: null,
                created_at: new Date().toISOString(),
              },
            ],
          },
        }),
      ),
      http.get('/api/v1/runs', () => HttpResponse.json({ data: RUNS })),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: [] })),
    )

    renderDashboard(makeClient())

    await waitFor(() => {
      expect(screen.getByText('NEEDS ATTENTION')).toBeInTheDocument()
    })
  })
})

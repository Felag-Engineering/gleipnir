import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import DashboardPage from './DashboardPage'
import type { ApiStats, ApiTimeSeriesResponse, ApiAttentionResponse, ApiRunsResponse, ApiPolicyListItem } from '@/api/types'

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

const STUB_POLICY: ApiPolicyListItem = {
  id: 'p1',
  name: 'test-policy',
  trigger_type: 'manual',
  folder: '',
  model: 'claude-3',
  tool_count: 0,
  tool_refs: [],
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  paused_at: null,
  latest_run: null,
  avg_token_cost: 0,
  run_count: 0,
  next_fire_at: null,
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

// Default handlers represent a fully-onboarded instance (nextStep === 'ready')
// so existing "No runs yet" assertions remain valid and the SetupChecklist is hidden.
function setupDefaultHandlers() {
  server.use(
    http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
    http.get('/api/v1/stats/timeseries', () => HttpResponse.json({ data: TIMESERIES })),
    http.get('/api/v1/attention', () => HttpResponse.json({ data: ATTENTION })),
    http.get('/api/v1/runs', () => HttpResponse.json({ data: RUNS })),
    http.get('/api/v1/mcp/servers', () =>
      HttpResponse.json({ data: [{ id: 's1', name: 'my-server', url: 'http://localhost:9000', tool_count: 1 }] }),
    ),
    http.get('/api/v1/models', () =>
      HttpResponse.json({ data: [{ provider: 'anthropic', models: [{ name: 'm1', display_name: 'Claude' }] }] }),
    ),
    http.get('/api/v1/policies', () =>
      HttpResponse.json({ data: [STUB_POLICY] }),
    ),
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

  it('shows empty-state messages in both charts when there are no runs', async () => {
    setupDefaultHandlers()
    renderDashboard(makeClient())

    await waitFor(() => {
      expect(screen.getByText('No runs in the last 24h')).toBeInTheDocument()
    })
    // 'No runs yet' also appears in RecentRunsFeed when runs are empty,
    // so use getAllByText and confirm at least one instance is present.
    expect(screen.getAllByText('No runs yet').length).toBeGreaterThanOrEqual(1)
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

  it('removes the Recharts measurement span from document.body on unmount', async () => {
    setupDefaultHandlers()

    // Recharts is fully mocked in this test file so the span never appears
    // naturally. Inject it manually to simulate what Recharts does at runtime.
    const span = document.createElement('span')
    span.id = 'recharts_measurement_span'
    span.textContent = '18:00'
    document.body.appendChild(span)

    const { unmount } = renderDashboard(makeClient())
    await waitFor(() => {
      expect(screen.getByText('Control Center')).toBeInTheDocument()
    })

    expect(document.getElementById('recharts_measurement_span')).not.toBeNull()

    unmount()

    expect(document.getElementById('recharts_measurement_span')).toBeNull()
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
      http.get('/api/v1/mcp/servers', () =>
        HttpResponse.json({ data: [{ id: 's1', name: 'my-server', url: 'http://localhost:9000', tool_count: 1 }] }),
      ),
      http.get('/api/v1/models', () =>
        HttpResponse.json({ data: [{ provider: 'anthropic', models: [{ name: 'm1', display_name: 'Claude' }] }] }),
      ),
      http.get('/api/v1/policies', () =>
        HttpResponse.json({ data: [STUB_POLICY] }),
      ),
    )

    renderDashboard(makeClient())

    await waitFor(() => {
      expect(screen.getByText('NEEDS ATTENTION')).toBeInTheDocument()
    })
  })

  it('RecentRunsFeed empty state shows "Start by adding a model API key" when no model is configured', async () => {
    server.use(
      http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
      http.get('/api/v1/stats/timeseries', () => HttpResponse.json({ data: TIMESERIES })),
      http.get('/api/v1/attention', () => HttpResponse.json({ data: ATTENTION })),
      http.get('/api/v1/runs', () => HttpResponse.json({ data: RUNS })),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/models', () =>
        HttpResponse.json({ data: [{ provider: 'anthropic', models: [] }] }),
      ),
      http.get('/api/v1/policies', () => HttpResponse.json({ data: [] })),
    )

    renderDashboard(makeClient())

    await waitFor(() => {
      expect(screen.getByText('Start by adding a model API key')).toBeInTheDocument()
    })
    // Both the SetupChecklist and the RecentRunsFeed empty state may render the same CTA,
    // so use getAllByRole and verify at least one points to the correct href.
    const modelsLinks = screen.getAllByRole('link', { name: 'Go to Models' })
    expect(modelsLinks.every(l => l.getAttribute('href') === '/admin/models')).toBe(true)
  })

  it('RecentRunsFeed empty state shows "Add an MCP server" when model is set but no server', async () => {
    server.use(
      http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
      http.get('/api/v1/stats/timeseries', () => HttpResponse.json({ data: TIMESERIES })),
      http.get('/api/v1/attention', () => HttpResponse.json({ data: ATTENTION })),
      http.get('/api/v1/runs', () => HttpResponse.json({ data: RUNS })),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/models', () =>
        HttpResponse.json({ data: [{ provider: 'anthropic', models: [{ name: 'm1', display_name: 'Claude' }] }] }),
      ),
      http.get('/api/v1/policies', () => HttpResponse.json({ data: [] })),
    )

    renderDashboard(makeClient())

    await waitFor(() => {
      expect(screen.getByText('Add an MCP server to give agents tools')).toBeInTheDocument()
    })
    const toolsLinks = screen.getAllByRole('link', { name: 'Go to Tools' })
    expect(toolsLinks.every(l => l.getAttribute('href') === '/tools')).toBe(true)
  })

  it('RecentRunsFeed empty state shows "Create your first agent" when model+server set but no agents', async () => {
    server.use(
      http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
      http.get('/api/v1/stats/timeseries', () => HttpResponse.json({ data: TIMESERIES })),
      http.get('/api/v1/attention', () => HttpResponse.json({ data: ATTENTION })),
      http.get('/api/v1/runs', () => HttpResponse.json({ data: RUNS })),
      http.get('/api/v1/mcp/servers', () =>
        HttpResponse.json({ data: [{ id: 's1', name: 'my-server', url: 'http://localhost:9000', tool_count: 1 }] }),
      ),
      http.get('/api/v1/models', () =>
        HttpResponse.json({ data: [{ provider: 'anthropic', models: [{ name: 'm1', display_name: 'Claude' }] }] }),
      ),
      http.get('/api/v1/policies', () => HttpResponse.json({ data: [] })),
    )

    renderDashboard(makeClient())

    await waitFor(() => {
      expect(screen.getByText('Create your first agent')).toBeInTheDocument()
    })
    const agentLinks = screen.getAllByRole('link', { name: 'New Agent' })
    expect(agentLinks.every(l => l.getAttribute('href') === '/agents/new')).toBe(true)
  })

  it('RecentRunsFeed empty state shows "No runs yet" when all setup steps are complete', async () => {
    setupDefaultHandlers()
    renderDashboard(makeClient())

    await waitFor(() => {
      expect(screen.getAllByText('No runs yet').length).toBeGreaterThanOrEqual(1)
    })
    const agentsLinks = screen.getAllByRole('link', { name: 'Go to Agents' })
    expect(agentsLinks.some(l => l.getAttribute('href') === '/agents')).toBe(true)
  })

  it('SetupChecklist is visible when any setup step is pending', async () => {
    server.use(
      http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
      http.get('/api/v1/stats/timeseries', () => HttpResponse.json({ data: TIMESERIES })),
      http.get('/api/v1/attention', () => HttpResponse.json({ data: ATTENTION })),
      http.get('/api/v1/runs', () => HttpResponse.json({ data: RUNS })),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/models', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/policies', () => HttpResponse.json({ data: [] })),
    )

    renderDashboard(makeClient())

    await waitFor(() => {
      expect(screen.getByText('SETUP')).toBeInTheDocument()
    })
  })

  it('SetupChecklist is absent when all four setup steps are complete', async () => {
    server.use(
      http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
      http.get('/api/v1/stats/timeseries', () => HttpResponse.json({ data: TIMESERIES })),
      http.get('/api/v1/attention', () => HttpResponse.json({ data: ATTENTION })),
      http.get('/api/v1/runs', () =>
        HttpResponse.json({ data: { runs: [{ id: 'r1', status: 'complete', policy_id: 'p1', policy_name: 'test', created_at: '2026-01-01T00:00:00Z', started_at: null, completed_at: null, token_cost: 0 }], total: 1 } }),
      ),
      http.get('/api/v1/mcp/servers', () =>
        HttpResponse.json({ data: [{ id: 's1', name: 'my-server', url: 'http://localhost:9000', tool_count: 1 }] }),
      ),
      http.get('/api/v1/models', () =>
        HttpResponse.json({ data: [{ provider: 'anthropic', models: [{ name: 'm1', display_name: 'Claude' }] }] }),
      ),
      http.get('/api/v1/policies', () =>
        HttpResponse.json({ data: [STUB_POLICY] }),
      ),
    )

    renderDashboard(makeClient())

    // Wait for all queries to settle — once the SETUP header disappears, all
    // readiness checks have resolved and determined every step is complete.
    await waitFor(() => {
      expect(screen.queryByText('SETUP')).not.toBeInTheDocument()
    })
  })
})

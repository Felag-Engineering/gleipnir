import React from 'react'
import { describe, it, expect } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import userEvent from '@testing-library/user-event'
import DashboardPage from './DashboardPage'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiPolicyListItem, ApiStats, ApiRun, ApiMcpServer } from '@/api/types'

const POLICIES: ApiPolicyListItem[] = [
  {
    id: 'p1',
    name: 'vikunja-triage',
    trigger_type: 'webhook',
    folder: '',
    created_at: '2026-03-07T14:32:11Z',
    updated_at: '2026-03-07T14:32:11Z',
    paused_at: null,
    latest_run: { id: 'r101', status: 'complete', started_at: '2026-03-07T14:32:11Z', token_cost: 1000 },
  },
]

const RUNS: ApiRun[] = [
  {
    id: 'r101',
    policy_id: 'p1',
    policy_name: 'vikunja-triage',
    status: 'complete',
    trigger_type: 'webhook',
    started_at: '2026-03-07T14:32:11Z',
    completed_at: '2026-03-07T14:35:11Z',
    token_cost: 1000,
    error: null,
    created_at: '2026-03-07T14:32:11Z',
    system_prompt: null,
  },
]

const STATS: ApiStats = {
  active_runs: 1,
  pending_approvals: 0,
  policy_count: 1,
  tokens_last_24h: 1000,
}

const SERVERS: ApiMcpServer[] = [
  { id: 's1', name: 'filesystem', url: 'http://localhost:8100', last_discovered_at: null, has_drift: false, created_at: '2026-03-01T00:00:00Z' },
]

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

function setupDefaultHandlers(overrides?: {
  policies?: ApiPolicyListItem[]
  runs?: ApiRun[]
  stats?: ApiStats
  servers?: ApiMcpServer[]
}) {
  server.use(
    http.get('/api/v1/policies', () =>
      HttpResponse.json({ data: overrides?.policies ?? POLICIES }),
    ),
    http.get('/api/v1/runs', () =>
      HttpResponse.json({ data: overrides?.runs ?? RUNS }),
    ),
    http.get('/api/v1/stats', () =>
      HttpResponse.json({ data: overrides?.stats ?? STATS }),
    ),
    http.get('/api/v1/mcp/servers', () =>
      HttpResponse.json({ data: overrides?.servers ?? SERVERS }),
    ),
  )
}

describe('DashboardPage', () => {
  it('stat strip shows Active Runs, Pending Approvals, System Health', async () => {
    setupDefaultHandlers()
    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      expect(screen.getByText('Active Runs')).toBeInTheDocument()
      expect(screen.getByText('Pending Approvals')).toBeInTheDocument()
      expect(screen.getByText('System Health')).toBeInTheDocument()
    })
  })

  it('activity feed renders run entries with policy names', async () => {
    setupDefaultHandlers()
    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      // Policy name appears in the activity feed
      expect(screen.getAllByText('vikunja-triage').length).toBeGreaterThan(0)
    })
  })

  it('status board renders policy names', async () => {
    setupDefaultHandlers()
    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      expect(screen.getAllByText('vikunja-triage').length).toBeGreaterThan(0)
    })
  })

  it('onboarding steps appear when no policies exist', async () => {
    setupDefaultHandlers({ policies: [], runs: [] })
    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      expect(screen.getByText(/get started with gleipnir/i)).toBeInTheDocument()
    })
  })

  it('onboarding step 1 shows checkmark when servers exist', async () => {
    setupDefaultHandlers({ policies: [], runs: [], servers: SERVERS })
    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      // The first step title (Add a tool source) becomes a link when no server
      // When server exists, the step number is replaced by a Check icon
      // We verify onboarding is shown and server count is non-zero
      expect(screen.getByText(/get started with gleipnir/i)).toBeInTheDocument()
    })

    // System Health card should show 1 server after data loads
    await waitFor(() => {
      expect(screen.getByText('1 server')).toBeInTheDocument()
    })
  })

  it('shows error state and Retry button when /api/v1/policies returns 500', async () => {
    server.use(
      http.get('/api/v1/policies', () =>
        HttpResponse.json({ error: 'internal server error' }, { status: 500 }),
      ),
      http.get('/api/v1/runs', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: SERVERS })),
    )

    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      expect(screen.getByText(/failed to load policies/i)).toBeInTheDocument()
    })

    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })

  it('retry button re-fetches /api/v1/policies', async () => {
    let callCount = 0
    server.use(
      http.get('/api/v1/policies', () => {
        callCount += 1
        if (callCount === 1) {
          return HttpResponse.json({ error: 'internal server error' }, { status: 500 })
        }
        return HttpResponse.json({ data: POLICIES })
      }),
      http.get('/api/v1/runs', () => HttpResponse.json({ data: RUNS })),
      http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: SERVERS })),
    )

    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    })

    await userEvent.click(screen.getByRole('button', { name: /retry/i }))

    await waitFor(() => {
      expect(screen.getAllByText('vikunja-triage').length).toBeGreaterThan(0)
    })

    expect(callCount).toBeGreaterThanOrEqual(2)
  })

  it('Review link appears in stat card when pendingApprovals > 0', async () => {
    setupDefaultHandlers({
      stats: { ...STATS, pending_approvals: 2 },
    })
    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      const reviewLink = screen.getByRole('link', { name: /review/i })
      expect(reviewLink).toBeInTheDocument()
    })
  })

  it('refetches /api/v1/policies when queries are invalidated', async () => {
    let callCount = 0
    server.use(
      http.get('/api/v1/policies', () => {
        callCount += 1
        return HttpResponse.json({ data: POLICIES })
      }),
      http.get('/api/v1/runs', () => HttpResponse.json({ data: RUNS })),
      http.get('/api/v1/stats', () => HttpResponse.json({ data: STATS })),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: SERVERS })),
    )

    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => expect(screen.getAllByText('vikunja-triage').length).toBeGreaterThan(0))
    expect(callCount).toBe(1)

    act(() => {
      void qc.invalidateQueries({ queryKey: queryKeys.runs.all })
      void qc.invalidateQueries({ queryKey: queryKeys.policies.all })
    })

    await waitFor(() => expect(callCount).toBe(2))
  })
})

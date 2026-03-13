import React from 'react'
import { describe, it, expect } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse, delay } from 'msw'
import { server } from '@/test/server'
import userEvent from '@testing-library/user-event'
import DashboardPage from './DashboardPage'
import type { ApiPolicyListItem, ApiStats } from '@/api/types'

function makeStats(policies: ApiPolicyListItem[]): ApiStats {
  const activeRuns = policies.filter(p => p.latest_run?.status === 'running').length
  const pendingApprovals = policies.filter(p => p.latest_run?.status === 'waiting_for_approval').length
  const tokensToday = policies.reduce((sum, p) => sum + (p.latest_run?.token_cost ?? 0), 0)
  return {
    active_runs: activeRuns,
    pending_approvals: pendingApprovals,
    policy_count: policies.length,
    tokens_last_24h: tokensToday,
  }
}

// status: 'complete' avoids the running-spinner aria-hidden contamination in Test 1
const POLICIES_COMPLETE: ApiPolicyListItem[] = [
  {
    id: 'p1',
    name: 'vikunja-triage',
    trigger_type: 'webhook',
    folder: '',
    created_at: '2026-03-07T14:32:11Z',
    updated_at: '2026-03-07T14:32:11Z',
    latest_run: { id: 'r101', status: 'complete', started_at: '2026-03-07T14:32:11Z', token_cost: 1000 },
  },
]

const POLICIES_INITIAL: ApiPolicyListItem[] = [
  {
    id: 'p1',
    name: 'vikunja-triage',
    trigger_type: 'webhook',
    folder: '',
    created_at: '2026-03-07T14:32:11Z',
    updated_at: '2026-03-07T14:32:11Z',
    latest_run: { id: 'r101', status: 'running', started_at: '2026-03-07T14:32:11Z', token_cost: 1000 },
  },
]

const POLICIES_UPDATED: ApiPolicyListItem[] = [
  ...POLICIES_INITIAL,
  {
    id: 'p2',
    name: 'grafana-alert-responder',
    trigger_type: 'poll',
    folder: '',
    created_at: '2026-03-07T14:00:00Z',
    updated_at: '2026-03-07T14:00:00Z',
    latest_run: { id: 'r201', status: 'running', started_at: '2026-03-07T14:30:00Z', token_cost: 2000 },
  },
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

describe('DashboardPage', () => {
  it('shows skeleton blocks while /api/v1/policies is in flight, then hides them once data arrives', async () => {
    server.use(
      http.get('/api/v1/policies', async () => {
        await delay(200)
        return HttpResponse.json({ data: POLICIES_COMPLETE })
      }),
      http.get('/api/v1/stats', () => {
        return HttpResponse.json({ data: makeStats(POLICIES_COMPLETE) })
      }),
    )

    const qc = makeClient()
    const { container } = renderDashboard(qc)

    // Before response resolves — skeletons should be visible
    const skeletonsBefore = container.querySelectorAll('[aria-hidden="true"]')
    expect(skeletonsBefore.length).toBeGreaterThan(0)

    // Wait for data to arrive
    await waitFor(() => expect(screen.getByText('vikunja-triage')).toBeInTheDocument())

    // After data loads — no skeletons remain (fixture uses 'complete' status, no spinner)
    const skeletonsAfter = container.querySelectorAll('[aria-hidden="true"]')
    expect(skeletonsAfter.length).toBe(0)
  })

  it("refetches /api/v1/policies when ['policies'] query is invalidated", async () => {
    let callCount = 0

    server.use(
      http.get('/api/v1/policies', () => {
        callCount += 1
        return HttpResponse.json({ data: POLICIES_INITIAL })
      }),
      http.get('/api/v1/stats', () => {
        return HttpResponse.json({ data: makeStats(POLICIES_INITIAL) })
      }),
    )

    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => expect(screen.getByText('vikunja-triage')).toBeInTheDocument())
    expect(callCount).toBe(1)

    act(() => {
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['policies'] })
    })

    await waitFor(() => expect(callCount).toBe(2))
  })

  it('StatsBar reflects updated counts after invalidation-triggered refetch', async () => {
    let callCount = 0
    let statsCallCount = 0

    server.use(
      http.get('/api/v1/policies', () => {
        callCount += 1
        const data = callCount === 1 ? POLICIES_INITIAL : POLICIES_UPDATED
        return HttpResponse.json({ data })
      }),
      http.get('/api/v1/stats', () => {
        statsCallCount += 1
        const data = statsCallCount === 1 ? POLICIES_INITIAL : POLICIES_UPDATED
        return HttpResponse.json({ data: makeStats(data) })
      }),
    )

    const qc = makeClient()
    renderDashboard(qc)

    // Wait for initial load — 1 policy
    await waitFor(() => expect(screen.getByText('vikunja-triage')).toBeInTheDocument())
    // StatsBar "Policies" card shows 1
    const policiesCard = screen.getByText('Policies').parentElement
    expect(policiesCard?.textContent).toContain('1')

    act(() => {
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['policies'] })
      void qc.invalidateQueries({ queryKey: ['stats'] })
    })

    // Wait for refetch — now 2 policies
    await waitFor(() => expect(screen.getByText('grafana-alert-responder')).toBeInTheDocument())
    // StatsBar "Policies" card now shows 2
    expect(screen.getByText('Policies').parentElement?.textContent).toContain('2')
  })

  it('shows error state and Retry button when /api/v1/policies returns 500', async () => {
    server.use(
      http.get('/api/v1/policies', () => {
        return HttpResponse.json({ error: 'internal server error' }, { status: 500 })
      }),
      http.get('/api/v1/stats', () => {
        return HttpResponse.json({ data: makeStats([]) })
      }),
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
        return HttpResponse.json({ data: POLICIES_COMPLETE })
      }),
      http.get('/api/v1/stats', () => {
        return HttpResponse.json({ data: makeStats(POLICIES_COMPLETE) })
      }),
    )

    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    })

    await userEvent.click(screen.getByRole('button', { name: /retry/i }))

    await waitFor(() => {
      expect(screen.getByText('vikunja-triage')).toBeInTheDocument()
    })

    expect(callCount).toBeGreaterThanOrEqual(2)
  })

  it('shows approval banner when a policy has a run waiting_for_approval', async () => {
    const policiesWithApproval: ApiPolicyListItem[] = [
      {
        id: 'p1',
        name: 'my-policy',
        trigger_type: 'webhook',
        folder: '',
        created_at: '2026-03-07T14:32:11Z',
        updated_at: '2026-03-07T14:32:11Z',
        latest_run: { id: 'r1', status: 'waiting_for_approval', started_at: '2026-03-07T14:32:11Z', token_cost: 500 },
      },
    ]

    server.use(
      http.get('/api/v1/policies', () => {
        return HttpResponse.json({ data: policiesWithApproval })
      }),
      http.get('/api/v1/stats', () => {
        return HttpResponse.json({ data: makeStats(policiesWithApproval) })
      }),
    )

    const qc = makeClient()
    renderDashboard(qc)

    await waitFor(() => {
      expect(screen.getByText('my-policy')).toBeInTheDocument()
    })

    // ApprovalBanner renders with role="status" when count > 0
    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(screen.getByRole('status').textContent).toContain('awaiting approval')
  })
})

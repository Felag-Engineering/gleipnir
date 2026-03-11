import React from 'react'
import { describe, it, expect } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse, delay } from 'msw'
import { server } from '@/test/server'
import DashboardPage from './DashboardPage'
import type { ApiPolicyListItem } from '@/api/types'

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

    server.use(
      http.get('/api/v1/policies', () => {
        callCount += 1
        const data = callCount === 1 ? POLICIES_INITIAL : POLICIES_UPDATED
        return HttpResponse.json({ data })
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
    })

    // Wait for refetch — now 2 policies
    await waitFor(() => expect(screen.getByText('grafana-alert-responder')).toBeInTheDocument())
    // StatsBar "Policies" card now shows 2
    expect(screen.getByText('Policies').parentElement?.textContent).toContain('2')
  })
})

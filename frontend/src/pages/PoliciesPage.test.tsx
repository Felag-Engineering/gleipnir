import React from 'react'
import { describe, it, expect } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse, delay } from 'msw'
import { server } from '@/test/server'
import userEvent from '@testing-library/user-event'
import PoliciesPage from './PoliciesPage'
import type { ApiPolicyListItem } from '@/api/types'

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

function makeClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderPage(queryClient: QueryClient) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('PoliciesPage', () => {
  it('shows skeleton blocks while /api/v1/policies is in flight, then shows policies', async () => {
    server.use(
      http.get('/api/v1/policies', async () => {
        await delay(200)
        return HttpResponse.json({ data: POLICIES })
      }),
    )

    const qc = makeClient()
    const { container } = renderPage(qc)

    // Skeletons visible before response resolves
    const skeletonsBefore = container.querySelectorAll('[aria-hidden="true"]')
    expect(skeletonsBefore.length).toBeGreaterThan(0)

    // Wait for data to arrive
    await waitFor(() => expect(screen.getByText('vikunja-triage')).toBeInTheDocument())
  })

  it('shows empty state when no policies exist', async () => {
    server.use(
      http.get('/api/v1/policies', () => {
        return HttpResponse.json({ data: [] })
      }),
    )

    const qc = makeClient()
    renderPage(qc)

    await waitFor(() => {
      expect(screen.getByText('No policies yet')).toBeInTheDocument()
    })

    expect(screen.getByText(/create your first policy/i)).toBeInTheDocument()

    // CTA links to /policies/new
    const ctaLinks = screen.getAllByRole('link', { name: /new policy/i })
    const ctaToNew = ctaLinks.some(l => l.getAttribute('href') === '/policies/new')
    expect(ctaToNew).toBe(true)
  })

  it('policy rows link to /policies/:id (editor), not /policies/:id/runs', async () => {
    server.use(
      http.get('/api/v1/policies', () => {
        return HttpResponse.json({ data: POLICIES })
      }),
    )

    const qc = makeClient()
    renderPage(qc)

    await waitFor(() => expect(screen.getByText('vikunja-triage')).toBeInTheDocument())

    const policyLink = screen.getByText('vikunja-triage').closest('a')
    expect(policyLink).toHaveAttribute('href', '/policies/p1')
    expect(policyLink).not.toHaveAttribute('href', '/policies/p1/runs')
  })

  it('"New Policy" button in header links to /policies/new', async () => {
    server.use(
      http.get('/api/v1/policies', () => {
        return HttpResponse.json({ data: POLICIES })
      }),
    )

    const qc = makeClient()
    renderPage(qc)

    await waitFor(() => expect(screen.getByText('vikunja-triage')).toBeInTheDocument())

    // The header New Policy link is a direct child of the header section
    const headerLink = screen.getByRole('link', { name: 'New Policy' })
    expect(headerLink).toHaveAttribute('href', '/policies/new')
  })

  it('shows error state with Retry button on API failure', async () => {
    server.use(
      http.get('/api/v1/policies', () => {
        return HttpResponse.json({ error: 'internal server error' }, { status: 500 })
      }),
    )

    const qc = makeClient()
    renderPage(qc)

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
    )

    const qc = makeClient()
    renderPage(qc)

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    })

    await userEvent.click(screen.getByRole('button', { name: /retry/i }))

    await waitFor(() => {
      expect(screen.getByText('vikunja-triage')).toBeInTheDocument()
    })

    expect(callCount).toBeGreaterThanOrEqual(2)
  })
})

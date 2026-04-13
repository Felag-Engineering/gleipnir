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
    model: '',
    tool_count: 0,
    tool_refs: [],
    created_at: '2026-03-07T14:32:11Z',
    updated_at: '2026-03-07T14:32:11Z',
    paused_at: null,
    latest_run: { id: 'r101', status: 'complete', started_at: '2026-03-07T14:32:11Z', token_cost: 1000 },
    avg_token_cost: 0,
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
  it('shows skeleton blocks while /api/v1/policies is in flight, then shows agents', async () => {
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

  it('shows empty state when no agents exist', async () => {
    server.use(
      http.get('/api/v1/policies', () => {
        return HttpResponse.json({ data: [] })
      }),
    )

    const qc = makeClient()
    renderPage(qc)

    await waitFor(() => {
      expect(screen.getByText('No agents yet')).toBeInTheDocument()
    })

    expect(screen.getByText(/create your first agent/i)).toBeInTheDocument()

    // CTA links to /agents/new
    const ctaLinks = screen.getAllByRole('link', { name: /new agent/i })
    const ctaToNew = ctaLinks.some(l => l.getAttribute('href') === '/agents/new')
    expect(ctaToNew).toBe(true)
  })

  it('edit button links to /agents/:id (editor)', async () => {
    server.use(
      http.get('/api/v1/policies', () => {
        return HttpResponse.json({ data: POLICIES })
      }),
    )

    const qc = makeClient()
    renderPage(qc)

    await waitFor(() => expect(screen.getByText('vikunja-triage')).toBeInTheDocument())

    const editLink = screen.getByRole('link', { name: /edit vikunja-triage/i })
    expect(editLink).toHaveAttribute('href', '/agents/p1')
  })

  it('"New Agent" button in header links to /agents/new', async () => {
    server.use(
      http.get('/api/v1/policies', () => {
        return HttpResponse.json({ data: POLICIES })
      }),
    )

    const qc = makeClient()
    renderPage(qc)

    await waitFor(() => expect(screen.getByText('vikunja-triage')).toBeInTheDocument())

    // The header New Agent link is a direct child of the header section
    const headerLink = screen.getByRole('link', { name: 'New Agent' })
    expect(headerLink).toHaveAttribute('href', '/agents/new')
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
      expect(screen.getByText(/failed to load agents/i)).toBeInTheDocument()
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

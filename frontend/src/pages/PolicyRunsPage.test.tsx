import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'

import PolicyRunsPage from './PolicyRunsPage'
import type { ApiRun } from '@/api/types'
import type { ApiPolicyDetail } from '@/api/types'

// --- Mocks ---

vi.mock('@/hooks/usePolicyRuns')
vi.mock('@/hooks/usePolicy')

import { usePolicyRuns } from '@/hooks/usePolicyRuns'
import { usePolicy } from '@/hooks/usePolicy'

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function makeRun(overrides?: Partial<ApiRun>): ApiRun {
  return {
    id: 'run-abcdef12-full-id',
    policy_id: 'p1',
    policy_name: 'my-policy',
    status: 'complete',
    trigger_type: 'webhook',
    trigger_payload: '{}',
    started_at: new Date(Date.now() - 120_000).toISOString(),
    completed_at: new Date(Date.now() - 60_000).toISOString(),
    token_cost: 1500,
    error: null,
    created_at: new Date().toISOString(),
    ...overrides,
  }
}

function makePolicy(overrides?: Partial<ApiPolicyDetail>): ApiPolicyDetail {
  return {
    id: 'p1',
    name: 'My Test Policy',
    trigger_type: 'webhook',
    folder: '',
    yaml: '',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...overrides,
  }
}

function renderPage(queryClient = makeQueryClient()) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={['/policies/p1/runs']}>
        <Routes>
          <Route path="/policies/:id/runs" element={<PolicyRunsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function mockPending() {
  vi.mocked(usePolicyRuns).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof usePolicyRuns>)

  vi.mocked(usePolicy).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof usePolicy>)
}

function mockLoaded(runs: ApiRun[], policy?: ApiPolicyDetail) {
  vi.mocked(usePolicyRuns).mockReturnValue({
    data: runs,
    status: 'success',
  } as ReturnType<typeof usePolicyRuns>)

  vi.mocked(usePolicy).mockReturnValue({
    data: policy ?? makePolicy(),
    status: 'success',
  } as ReturnType<typeof usePolicy>)
}

function mockError() {
  vi.mocked(usePolicyRuns).mockReturnValue({
    data: undefined,
    status: 'error',
  } as ReturnType<typeof usePolicyRuns>)

  vi.mocked(usePolicy).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof usePolicy>)
}

// --- Tests ---

describe('PolicyRunsPage — skeleton on load', () => {
  beforeEach(() => {
    mockPending()
  })

  it('renders skeleton blocks while data is pending', () => {
    renderPage()
    const skeletons = document.querySelectorAll('[aria-hidden="true"]')
    expect(skeletons.length).toBeGreaterThan(0)
  })
})

describe('PolicyRunsPage — run rows', () => {
  it('renders a row for each run', () => {
    const runs = [
      makeRun({ id: 'aaaabbbb-cccc-dddd-eeee-ffffffffffff' }),
      makeRun({ id: 'bbbbcccc-dddd-eeee-ffff-000000000000', status: 'failed' }),
    ]
    mockLoaded(runs)
    renderPage()
    // Two truncated IDs visible
    expect(screen.getByTitle('aaaabbbb-cccc-dddd-eeee-ffffffffffff')).toBeInTheDocument()
    expect(screen.getByTitle('bbbbcccc-dddd-eeee-ffff-000000000000')).toBeInTheDocument()
  })

  it('each run row links to /runs/:run_id', () => {
    const run = makeRun({ id: 'aaaabbbb-cccc-dddd-eeee-ffffffffffff' })
    mockLoaded([run])
    renderPage()
    const link = screen.getByTitle('aaaabbbb-cccc-dddd-eeee-ffffffffffff').closest('a')
    expect(link).toHaveAttribute('href', '/runs/aaaabbbb-cccc-dddd-eeee-ffffffffffff')
  })

  it('run ID is truncated to 8 chars in the row with full ID in title', () => {
    const run = makeRun({ id: 'aaaabbbb-cccc-dddd-eeee-ffffffffffff' })
    mockLoaded([run])
    renderPage()
    const idCell = screen.getByTitle('aaaabbbb-cccc-dddd-eeee-ffffffffffff')
    expect(idCell.textContent).toBe('aaaabbbb')
  })

  it('renders StatusBadge with the run status', () => {
    const run = makeRun({ status: 'failed' })
    mockLoaded([run])
    renderPage()
    expect(screen.getByText('Failed')).toBeInTheDocument()
  })

  it('renders TriggerChip with the run trigger type', () => {
    const run = makeRun({ trigger_type: 'webhook' })
    mockLoaded([run])
    renderPage()
    expect(screen.getByText('webhook')).toBeInTheDocument()
  })
})

describe('PolicyRunsPage — empty state', () => {
  it('shows empty state when runs list is empty', () => {
    mockLoaded([])
    renderPage()
    expect(screen.getByText('No runs yet')).toBeInTheDocument()
    expect(screen.getByText(/trigger this policy/i)).toBeInTheDocument()
  })
})

describe('PolicyRunsPage — error state', () => {
  it('shows error message when usePolicyRuns status is error', () => {
    mockError()
    renderPage()
    expect(screen.getByText(/failed to load runs/i)).toBeInTheDocument()
  })

  it('shows retry button in error state', () => {
    mockError()
    renderPage()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })

  it('retry button calls queryClient.invalidateQueries', () => {
    mockError()
    const qc = makeQueryClient()
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={['/policies/p1/runs']}>
          <Routes>
            <Route path="/policies/:id/runs" element={<PolicyRunsPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )
    fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(invalidateSpy).toHaveBeenCalled()
  })
})

describe('PolicyRunsPage — navigation links', () => {
  beforeEach(() => {
    mockLoaded([])
  })

  it('back link points to /dashboard', () => {
    renderPage()
    const backLink = screen.getByText(/← Policies/)
    expect(backLink).toHaveAttribute('href', '/dashboard')
  })

  it('"Edit policy" link in header points to /policies/:id', () => {
    renderPage()
    // There may be multiple "Edit policy" links (header + empty state CTA).
    // The header edit link is the one outside the empty state container.
    const editLinks = screen.getAllByRole('link', { name: /edit policy/i })
    expect(editLinks.length).toBeGreaterThanOrEqual(1)
    // All edit policy links should point to the same destination
    for (const link of editLinks) {
      expect(link).toHaveAttribute('href', '/policies/p1')
    }
  })
})

describe('PolicyRunsPage — heading', () => {
  it('shows policy name from usePolicy when loaded', () => {
    mockLoaded([], makePolicy({ name: 'Fancy Policy Name' }))
    renderPage()
    expect(screen.getByRole('heading', { name: 'Fancy Policy Name' })).toBeInTheDocument()
  })

  it('falls back to id in heading when usePolicy is pending', () => {
    vi.mocked(usePolicyRuns).mockReturnValue({
      data: [],
      status: 'success',
    } as ReturnType<typeof usePolicyRuns>)

    vi.mocked(usePolicy).mockReturnValue({
      data: undefined,
      status: 'pending',
    } as ReturnType<typeof usePolicy>)

    renderPage()
    // Heading should show the route param id = 'p1'
    expect(screen.getByRole('heading', { name: 'p1' })).toBeInTheDocument()
  })
})

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'

import RunsPage from './RunsPage'
import { computePageNumbers } from '@/utils/pagination'
import type { ApiRun, ApiPolicyListItem } from '@/api/types'

// --- Mocks ---

vi.mock('@/hooks/queries/runs')
vi.mock('@/hooks/queries/policies')

import { useRuns } from '@/hooks/queries/runs'
import { usePolicies } from '@/hooks/queries/policies'

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function makeRun(overrides?: Partial<ApiRun>): ApiRun {
  return {
    id: 'run-abcdef12-full-id',
    policy_id: 'p1',
    policy_name: 'My Policy',
    status: 'complete',
    trigger_type: 'webhook',
    trigger_payload: '{}',
    started_at: new Date(Date.now() - 120_000).toISOString(),
    completed_at: new Date(Date.now() - 60_000).toISOString(),
    token_cost: 1500,
    error: null,
    created_at: new Date().toISOString(),
    system_prompt: null,
    model: '',
    ...overrides,
  }
}

function makePolicy(overrides?: Partial<ApiPolicyListItem>): ApiPolicyListItem {
  return {
    id: 'p1',
    name: 'My Policy',
    trigger_type: 'webhook',
    folder: '',
    model: '',
    tool_count: 0,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    paused_at: null,
    latest_run: null,
    avg_token_cost: 0,
    ...overrides,
  }
}

function renderPage(initialPath = '/runs') {
  const queryClient = makeQueryClient()
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route path="/runs" element={<RunsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function mockPending() {
  vi.mocked(useRuns).mockReturnValue({
    runs: [],
    total: 0,
    data: undefined,
    status: 'pending',
  } as unknown as ReturnType<typeof useRuns>)

  vi.mocked(usePolicies).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof usePolicies>)
}

function mockLoaded(runs: ApiRun[], total?: number, policies?: ApiPolicyListItem[]) {
  vi.mocked(useRuns).mockReturnValue({
    runs,
    total: total ?? runs.length,
    data: { runs, total: total ?? runs.length },
    status: 'success',
  } as unknown as ReturnType<typeof useRuns>)

  vi.mocked(usePolicies).mockReturnValue({
    data: policies ?? [makePolicy()],
    status: 'success',
  } as ReturnType<typeof usePolicies>)
}

function mockError() {
  vi.mocked(useRuns).mockReturnValue({
    runs: [],
    total: 0,
    data: undefined,
    status: 'error',
  } as unknown as ReturnType<typeof useRuns>)

  vi.mocked(usePolicies).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof usePolicies>)
}

// --- Tests ---

describe('RunsPage — heading', () => {
  beforeEach(() => {
    mockLoaded([])
  })

  it('renders heading "Run History"', () => {
    renderPage()
    expect(screen.getByRole('heading', { name: 'Run History' })).toBeInTheDocument()
  })
})

describe('RunsPage — loading state', () => {
  beforeEach(() => {
    mockPending()
  })

  it('renders skeleton blocks when loading', () => {
    renderPage()
    const skeletons = document.querySelectorAll('[aria-hidden="true"]')
    expect(skeletons.length).toBeGreaterThan(0)
  })
})

describe('RunsPage — run rows', () => {
  it('renders a row for each run with correct href', () => {
    const runs = [
      makeRun({ id: 'aaaabbbb-cccc-dddd-eeee-ffffffffffff' }),
      makeRun({ id: 'bbbbcccc-dddd-eeee-ffff-000000000000', status: 'failed' }),
    ]
    mockLoaded(runs)
    renderPage()

    // Each row is a Link; verify via href
    const links = screen.getAllByRole('link')
    const hrefs = links.map((l) => l.getAttribute('href'))
    expect(hrefs).toContain('/runs/aaaabbbb-cccc-dddd-eeee-ffffffffffff')
    expect(hrefs).toContain('/runs/bbbbcccc-dddd-eeee-ffff-000000000000')
  })

  it('shows policy name in row with title attribute for overflow tooltip', () => {
    const run = makeRun({ policy_name: 'My Special Policy' })
    mockLoaded([run])
    renderPage()
    expect(screen.getByTitle('My Special Policy')).toBeInTheDocument()
  })

  it('shows truncated run ID and trigger type in the subtext line', () => {
    const run = makeRun({ id: 'aaaabbbb-cccc-dddd-eeee-ffffffffffff', trigger_type: 'webhook' })
    mockLoaded([run])
    renderPage()
    // Subtext is "{id.slice(0,8)} · {trigger_type}"
    expect(screen.getByText(/aaaabbbb/)).toBeInTheDocument()
    expect(screen.getByText(/webhook/)).toBeInTheDocument()
  })

  it('falls back to policy_id when policy_name is undefined', () => {
    const run = makeRun({ policy_name: undefined, policy_id: 'policy-xyz-id' })
    mockLoaded([run])
    renderPage()
    expect(screen.getByText('policy-xyz-id')).toBeInTheDocument()
  })
})

describe('RunsPage — empty state', () => {
  it('shows EmptyState with "No runs found" when no runs', () => {
    mockLoaded([])
    renderPage()
    expect(screen.getByRole('heading', { name: /no runs found/i })).toBeInTheDocument()
  })

  it('empty state includes a link to Policies', () => {
    mockLoaded([])
    renderPage()
    const cta = screen.getByRole('link', { name: /go to policies/i })
    expect(cta).toHaveAttribute('href', '/policies')
  })
})

describe('RunsPage — error state', () => {
  it('shows error message when fetch fails', () => {
    mockError()
    renderPage()
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText(/failed to load runs/i)).toBeInTheDocument()
  })

  it('shows retry button in error state', () => {
    mockError()
    renderPage()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })
})

describe('RunsPage — filters', () => {
  beforeEach(() => {
    mockLoaded([makeRun()])
  })

  it('status filter radiogroup is present', () => {
    renderPage()
    expect(screen.getByRole('radiogroup', { name: /filter by status/i })).toBeInTheDocument()
  })

  it('all status filter chips are present with "All" checked by default', () => {
    renderPage()
    expect(screen.getByRole('radio', { name: 'All' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Complete' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Running' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Failed' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Approval' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'All' })).toHaveAttribute('aria-checked', 'true')
  })

  it('policy filter select is present', () => {
    renderPage()
    expect(screen.getByRole('combobox', { name: /filter by policy/i })).toBeInTheDocument()
  })

  it('date range filter select is present', () => {
    renderPage()
    expect(screen.getByRole('combobox', { name: /filter by date range/i })).toBeInTheDocument()
  })

  it('clicking a status chip marks it as checked', () => {
    renderPage()
    const completeChip = screen.getByRole('radio', { name: 'Complete' })
    fireEvent.click(completeChip)
    // After click, Complete chip should report checked
    expect(completeChip).toHaveAttribute('aria-checked', 'true')
  })
})

describe('RunsPage — pagination', () => {
  it('both page arrows are disabled on a single-page result', () => {
    mockLoaded([makeRun()], 10)
    renderPage()
    expect(screen.getByRole('button', { name: /next page/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /previous page/i })).toBeDisabled()
  })

  it('shows "X–Y of Z" page info when runs are present', () => {
    const runs = [makeRun(), makeRun({ id: 'run-2222222222222222' })]
    mockLoaded(runs, 2)
    renderPage()
    expect(screen.getByText('1–2 of 2')).toBeInTheDocument()
  })

  it('pagination is not shown when there are no runs', () => {
    mockLoaded([])
    renderPage()
    expect(screen.queryByText(/of \d/)).not.toBeInTheDocument()
  })

  it('page 1 button is rendered and active', () => {
    mockLoaded([makeRun()], 10)
    renderPage()
    const page1 = screen.getByRole('button', { name: 'Page 1' })
    expect(page1).toHaveAttribute('aria-current', 'page')
  })
})

describe('RunsPage — sort chip', () => {
  it('sort chip is present with default "Newest ▼" label', () => {
    mockLoaded([makeRun()])
    renderPage()
    expect(screen.getByRole('button', { name: /sort by date, currently newest first/i })).toBeInTheDocument()
  })

  it('sort chip toggles to "oldest" on click', () => {
    mockLoaded([makeRun()])
    renderPage()
    const sortBtn = screen.getByRole('button', { name: /sort by date, currently newest first/i })
    fireEvent.click(sortBtn)
    // After click, the aria-label should reflect the new sort direction
    expect(screen.getByRole('button', { name: /sort by date, currently oldest first/i })).toBeInTheDocument()
  })
})

describe('RunsPage — stats subtitle', () => {
  it('shows "{total} runs" in deferred mode when backend stats absent', () => {
    mockLoaded([makeRun()], 42)
    renderPage()
    expect(screen.getByText('42 runs')).toBeInTheDocument()
  })

  it('stats line is hidden when there are no runs', () => {
    mockLoaded([])
    renderPage()
    expect(screen.queryByText(/\d+ runs/)).not.toBeInTheDocument()
  })
})

// --- Unit tests for computePageNumbers ---

describe('computePageNumbers', () => {
  it('returns [1] for a single page', () => {
    expect(computePageNumbers(1, 1)).toEqual([1])
  })

  it('returns all pages when totalPages <= 7', () => {
    expect(computePageNumbers(1, 5)).toEqual([1, 2, 3, 4, 5])
    expect(computePageNumbers(3, 7)).toEqual([1, 2, 3, 4, 5, 6, 7])
  })

  it('inserts ellipsis when current page is in the middle of many pages', () => {
    const result = computePageNumbers(5, 10)
    expect(result).toEqual([1, 'ellipsis', 4, 5, 6, 'ellipsis', 10])
  })

  it('no leading ellipsis when current page is near the start', () => {
    const result = computePageNumbers(2, 10)
    expect(result[0]).toBe(1)
    expect(result[1]).toBe(2)
    expect(result[2]).toBe(3)
    // Should have trailing ellipsis since 4..9 are skipped
    expect(result).toContain('ellipsis')
    expect(result[result.length - 1]).toBe(10)
  })

  it('no trailing ellipsis when current page is near the end', () => {
    const result = computePageNumbers(9, 10)
    expect(result[0]).toBe(1)
    expect(result[result.length - 1]).toBe(10)
    expect(result[result.length - 2]).toBe(9)
    expect(result[result.length - 3]).toBe(8)
  })

  it('first page is always 1 and last page is always totalPages', () => {
    const result = computePageNumbers(5, 20)
    expect(result[0]).toBe(1)
    expect(result[result.length - 1]).toBe(20)
  })
})

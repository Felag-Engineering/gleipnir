import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PolicyCard } from './PolicyCard'
import type { ApiPolicyListItem, ApiPolicyDetail } from '@/api/types'

vi.mock('@/hooks/queries/policies')
vi.mock('@/hooks/queries/runs')

import { usePolicy } from '@/hooks/queries/policies'
import { useRuns } from '@/hooks/queries/runs'

const DETAIL_YAML = `
name: system-health-check
description: Monitors system health and alerts on anomalies.
model:
  provider: anthropic
  name: claude-sonnet-4-6
trigger:
  type: webhook
capabilities:
  tools:
    - tool: test-server.get_current_time
    - tool: test-server.list_files
    - tool: metrics.query_range
agent:
  task: Check everything.
  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 50
  concurrency: skip
`

const MOCK_DETAIL: ApiPolicyDetail = {
  id: 'p1',
  name: 'system-health-check',
  trigger_type: 'webhook',
  folder: '',
  yaml: DETAIL_YAML,
  created_at: '2026-04-03T14:00:00Z',
  updated_at: '2026-04-03T14:00:00Z',
  paused_at: null,
}

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderWithQuery(ui: React.ReactElement) {
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}

const POLICY: ApiPolicyListItem = {
  id: 'p1',
  name: 'system-health-check',
  trigger_type: 'webhook',
  folder: '',
  model: 'gemini-2.5-flash',
  tool_count: 5,
  tool_refs: [],
  avg_token_cost: 4500,
  created_at: '2026-04-03T14:00:00Z',
  updated_at: '2026-04-03T14:00:00Z',
  paused_at: null,
  latest_run: {
    id: 'r1',
    status: 'complete',
    started_at: '2026-04-03T12:00:00Z',
    token_cost: 4500,
  },
}

// Default mocks so existing collapsed-only tests don't throw when hooks are called on expand
beforeEach(() => {
  vi.mocked(usePolicy).mockReturnValue({
    data: undefined,
    isLoading: false,
  } as unknown as ReturnType<typeof usePolicy>)

  vi.mocked(useRuns).mockReturnValue({
    runs: [],
    total: 0,
    isLoading: false,
    data: undefined,
  } as unknown as ReturnType<typeof useRuns>)
})

describe('PolicyCard', () => {
  it('renders policy name, trigger, model, and tool count', () => {
    render(
      <MemoryRouter>
        <PolicyCard policy={POLICY} onTrigger={() => {}} />
      </MemoryRouter>,
    )
    expect(screen.getByText('system-health-check')).toBeInTheDocument()
    expect(screen.getByText('webhook')).toBeInTheDocument()
    expect(screen.getByText('gemini-2.5-flash')).toBeInTheDocument()
    expect(screen.getByText('5 tools')).toBeInTheDocument()
  })

  it('renders status badge and time for latest run', () => {
    render(
      <MemoryRouter>
        <PolicyCard policy={POLICY} onTrigger={() => {}} />
      </MemoryRouter>,
    )
    expect(screen.getByText('Complete')).toBeInTheDocument()
  })

  it('renders play and edit buttons', () => {
    const onTrigger = vi.fn()
    render(
      <MemoryRouter>
        <PolicyCard policy={POLICY} onTrigger={onTrigger} />
      </MemoryRouter>,
    )
    expect(screen.getByRole('button', { name: /run system-health-check/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /edit system-health-check/i })).toBeInTheDocument()
  })

  it('edit button links to /agents/:id', () => {
    render(
      <MemoryRouter>
        <PolicyCard policy={POLICY} onTrigger={() => {}} />
      </MemoryRouter>,
    )
    expect(screen.getByRole('link', { name: /edit system-health-check/i })).toHaveAttribute('href', '/agents/p1')
  })

  it('hides model pill when model is empty', () => {
    const noModel = { ...POLICY, model: '' }
    render(
      <MemoryRouter>
        <PolicyCard policy={noModel} onTrigger={() => {}} />
      </MemoryRouter>,
    )
    expect(screen.queryByText('gemini-2.5-flash')).toBeNull()
  })

  it('uses plural "tools" for count > 1', () => {
    render(
      <MemoryRouter>
        <PolicyCard policy={POLICY} onTrigger={() => {}} />
      </MemoryRouter>,
    )
    expect(screen.getByText('5 tools')).toBeInTheDocument()
  })

  it('uses singular "tool" for count of 1', () => {
    const oneTool = { ...POLICY, tool_count: 1 }
    render(
      <MemoryRouter>
        <PolicyCard policy={oneTool} onTrigger={() => {}} />
      </MemoryRouter>,
    )
    expect(screen.getByText('1 tool')).toBeInTheDocument()
  })

  it('hides tool count when zero', () => {
    const noTools = { ...POLICY, tool_count: 0 }
    render(
      <MemoryRouter>
        <PolicyCard policy={noTools} onTrigger={() => {}} />
      </MemoryRouter>,
    )
    expect(screen.queryByText(/tools/)).toBeNull()
  })

  it('renders with no latest run', () => {
    const noRunPolicy = { ...POLICY, latest_run: null }
    render(
      <MemoryRouter>
        <PolicyCard policy={noRunPolicy} onTrigger={() => {}} />
      </MemoryRouter>,
    )
    expect(screen.getByText('system-health-check')).toBeInTheDocument()
    expect(screen.queryByText('Complete')).toBeNull()
  })

  it('calls onTrigger when play button is clicked', async () => {
    const onTrigger = vi.fn()
    render(
      <MemoryRouter>
        <PolicyCard policy={POLICY} onTrigger={onTrigger} />
      </MemoryRouter>,
    )
    screen.getByRole('button', { name: /run system-health-check/i }).click()
    expect(onTrigger).toHaveBeenCalledWith('p1', 'system-health-check')
  })
})

describe('PolicyCard expanded', () => {
  it('shows loading state immediately after clicking the card', async () => {
    vi.mocked(usePolicy).mockReturnValue({
      data: undefined,
      isLoading: true,
    } as unknown as ReturnType<typeof usePolicy>)

    vi.mocked(useRuns).mockReturnValue({
      runs: [],
      total: 0,
      isLoading: true,
      data: undefined,
    } as unknown as ReturnType<typeof useRuns>)

    const user = userEvent.setup()
    renderWithQuery(<PolicyCard policy={POLICY} onTrigger={() => {}} />)

    await user.click(screen.getByText('system-health-check'))

    expect(screen.getByText('Loading...')).toBeInTheDocument()
  })

  it('shows stat bar labels after data loads', async () => {
    vi.mocked(usePolicy).mockReturnValue({
      data: MOCK_DETAIL,
      isLoading: false,
    } as unknown as ReturnType<typeof usePolicy>)

    vi.mocked(useRuns).mockReturnValue({
      runs: [],
      total: 0,
      isLoading: false,
      data: { runs: [], total: 0 },
    } as unknown as ReturnType<typeof useRuns>)

    const user = userEvent.setup()
    renderWithQuery(<PolicyCard policy={POLICY} onTrigger={() => {}} />)

    await user.click(screen.getByText('system-health-check'))

    await waitFor(() => {
      expect(screen.getByText('Avg Tokens')).toBeInTheDocument()
    })
    expect(screen.getByText('Limits')).toBeInTheDocument()
    expect(screen.getByText('Concurrency')).toBeInTheDocument()
  })

  it('renders description from parsed YAML', async () => {
    vi.mocked(usePolicy).mockReturnValue({
      data: MOCK_DETAIL,
      isLoading: false,
    } as unknown as ReturnType<typeof usePolicy>)

    vi.mocked(useRuns).mockReturnValue({
      runs: [],
      total: 0,
      isLoading: false,
      data: { runs: [], total: 0 },
    } as unknown as ReturnType<typeof useRuns>)

    const user = userEvent.setup()
    renderWithQuery(<PolicyCard policy={POLICY} onTrigger={() => {}} />)

    await user.click(screen.getByText('system-health-check'))

    await waitFor(() => {
      expect(screen.getByText('Monitors system health and alerts on anomalies.')).toBeInTheDocument()
    })
  })

  it('renders server-grouped capability pills', async () => {
    vi.mocked(usePolicy).mockReturnValue({
      data: MOCK_DETAIL,
      isLoading: false,
    } as unknown as ReturnType<typeof usePolicy>)

    vi.mocked(useRuns).mockReturnValue({
      runs: [],
      total: 0,
      isLoading: false,
      data: { runs: [], total: 0 },
    } as unknown as ReturnType<typeof useRuns>)

    const user = userEvent.setup()
    renderWithQuery(<PolicyCard policy={POLICY} onTrigger={() => {}} />)

    await user.click(screen.getByText('system-health-check'))

    await waitFor(() => {
      expect(screen.getByText('test-server (2)')).toBeInTheDocument()
    })
    expect(screen.getByText('metrics (1)')).toBeInTheDocument()
  })

  it('collapses on second click', async () => {
    vi.mocked(usePolicy).mockReturnValue({
      data: MOCK_DETAIL,
      isLoading: false,
    } as unknown as ReturnType<typeof usePolicy>)

    vi.mocked(useRuns).mockReturnValue({
      runs: [],
      total: 0,
      isLoading: false,
      data: { runs: [], total: 0 },
    } as unknown as ReturnType<typeof useRuns>)

    const user = userEvent.setup()
    renderWithQuery(<PolicyCard policy={POLICY} onTrigger={() => {}} />)

    const nameEl = screen.getByText('system-health-check')
    await user.click(nameEl)

    await waitFor(() => {
      expect(screen.getByText('Avg Tokens')).toBeInTheDocument()
    })

    await user.click(nameEl)

    await waitFor(() => {
      expect(screen.queryByText('Avg Tokens')).toBeNull()
    })
  })
})

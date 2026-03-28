import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'

import RunDetailPage from './RunDetailPage'
import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { CopyBlock } from '@/components/CopyBlock'
import type { ApiRun, ApiRunStep } from '@/api/types'

// --- Mocks ---

vi.mock('@/hooks/useRun')
vi.mock('@/hooks/useRunSteps')

import { useRun } from '@/hooks/useRun'
import { useRunSteps } from '@/hooks/useRunSteps'

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function makeRun(overrides?: Partial<ApiRun>): ApiRun {
  return {
    id: 'r1',
    policy_id: 'p1',
    policy_name: 'my-policy',
    status: 'complete',
    trigger_type: 'webhook',
    trigger_payload: '{}',
    started_at: new Date(Date.now() - 60_000).toISOString(),
    completed_at: new Date().toISOString(),
    token_cost: 1000,
    error: null,
    created_at: new Date().toISOString(),
    system_prompt: null,
    ...overrides,
  }
}

function makeStep(overrides?: Partial<ApiRunStep>): ApiRunStep {
  return {
    id: 's1',
    run_id: 'r1',
    step_number: 0,
    type: 'thought',
    content: JSON.stringify({ text: 'I am thinking.' }),
    token_cost: 50,
    created_at: new Date().toISOString(),
    ...overrides,
  }
}

function renderPage(queryClient = makeQueryClient()) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={['/runs/r1']}>
        <Routes>
          <Route path="/runs/:id" element={<RunDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function mockPending() {
  vi.mocked(useRun).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof useRun>)

  vi.mocked(useRunSteps).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof useRunSteps>)
}

function mockLoaded(run: ApiRun, steps: ApiRunStep[] = []) {
  vi.mocked(useRun).mockReturnValue({
    data: run,
    status: 'success',
  } as ReturnType<typeof useRun>)

  vi.mocked(useRunSteps).mockReturnValue({
    data: steps,
    status: 'success',
  } as ReturnType<typeof useRunSteps>)
}

// --- Tests ---

describe('RunDetailPage — skeleton on load', () => {
  beforeEach(() => {
    mockPending()
  })

  it('renders skeleton blocks while data is loading', () => {
    renderPage()
    // SkeletonBlock renders with aria-hidden="true"
    const skeletons = document.querySelectorAll('[aria-hidden="true"]')
    expect(skeletons.length).toBeGreaterThan(0)
  })

  it('does not render run header while pending', () => {
    renderPage()
    expect(screen.queryByRole('button', { name: /back/i })).not.toBeInTheDocument()
  })
})

describe('RunDetailPage — step types render', () => {
  it('renders thought step text', () => {
    mockLoaded(makeRun(), [
      makeStep({ type: 'thought', content: JSON.stringify({ text: 'Thinking hard.' }) }),
    ])
    renderPage()
    expect(screen.getByText('Thinking hard.')).toBeInTheDocument()
  })

  it('renders tool_call step with tool name and tool label', () => {
    mockLoaded(makeRun(), [
      makeStep({
        id: 's2',
        type: 'tool_call',
        content: JSON.stringify({ tool_name: 'fs.read', server_id: 'srv1', input: { path: '/tmp' } }),
      }),
    ])
    renderPage()
    expect(screen.getByText('fs.read')).toBeInTheDocument()
    expect(screen.getByText('tool call')).toBeInTheDocument()
  })

  it('renders tool_call step with tool label when capability_snapshot marks it as tool', () => {
    const capContent = JSON.stringify([
      { ServerName: 'srv1', ToolName: 'fs.write', Role: 'tool', Approval: 'none', Timeout: 0, OnTimeout: '' },
    ])
    mockLoaded(makeRun(), [
      makeStep({ id: 'snap', type: 'capability_snapshot', content: capContent }),
      makeStep({
        id: 's2',
        step_number: 1,
        type: 'tool_call',
        content: JSON.stringify({ tool_name: 'fs.write', server_id: 'srv1', input: { path: '/tmp' } }),
      }),
    ])
    renderPage()
    expect(screen.getByText('tool call')).toBeInTheDocument()
  })

  it('renders tool_result step', () => {
    mockLoaded(makeRun(), [
      makeStep({
        id: 's3',
        type: 'tool_result',
        content: JSON.stringify({ tool_name: 'fs.read', output: '"file contents"', is_error: false }),
      }),
    ])
    renderPage()
    expect(screen.getByText('fs.read')).toBeInTheDocument()
    expect(screen.getByText('result')).toBeInTheDocument()
  })

  it('renders error step with red label and message', () => {
    mockLoaded(makeRun(), [
      makeStep({
        id: 's4',
        type: 'error',
        content: JSON.stringify({ message: 'something went wrong', code: 'TIMEOUT' }),
      }),
    ])
    renderPage()
    expect(screen.getByText('something went wrong')).toBeInTheDocument()
    expect(screen.getByText('Error')).toBeInTheDocument()
  })

  it('renders complete step', () => {
    mockLoaded(makeRun(), [
      makeStep({
        id: 's5',
        type: 'complete',
        content: JSON.stringify({ message: 'Done successfully.' }),
      }),
    ])
    renderPage()
    // "Complete" appears in both the filter bar and step card; check message is there
    expect(screen.getByText('Done successfully.')).toBeInTheDocument()
    // At least one "Complete" label exists (the step type label)
    const completeLabels = screen.getAllByText('Complete')
    expect(completeLabels.length).toBeGreaterThanOrEqual(1)
  })

  it('renders capability_snapshot step collapsed by default', () => {
    const capContent = JSON.stringify([
      { ServerName: 'srv1', ToolName: 'fs.read', Role: 'tool', Approval: 'none', Timeout: 0, OnTimeout: '' },
    ])
    mockLoaded(makeRun(), [
      makeStep({ id: 'snap', type: 'capability_snapshot', content: capContent }),
    ])
    renderPage()
    expect(screen.getByText(/Capability snapshot/)).toBeInTheDocument()
    // Table should not be visible yet
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('expands capability_snapshot on click', async () => {
    const capContent = JSON.stringify([
      { ServerName: 'srv1', ToolName: 'fs.read', Role: 'tool', Approval: 'none', Timeout: 0, OnTimeout: '' },
    ])
    mockLoaded(makeRun(), [
      makeStep({ id: 'snap', type: 'capability_snapshot', content: capContent }),
    ])
    renderPage()
    fireEvent.click(screen.getByText(/Capability snapshot/))
    await waitFor(() => {
      expect(screen.getByRole('table')).toBeInTheDocument()
    })
  })

  it('renders capability_snapshot V2 with provider in summary label', () => {
    const capContent = JSON.stringify({
      provider: 'anthropic',
      model: 'claude-sonnet-4-6',
      tools: [
        { ServerName: 'srv1', ToolName: 'fs.read', Role: 'tool', Approval: 'none', Timeout: 0, OnTimeout: '' },
      ],
    })
    mockLoaded(makeRun(), [
      makeStep({ id: 'snap', type: 'capability_snapshot', content: capContent }),
    ])
    renderPage()
    const summary = screen.getByText(/Capability snapshot/)
    expect(summary.textContent).toContain('anthropic')
    expect(summary.textContent).toContain('claude-sonnet-4-6')
  })

  it('renders capability_snapshot V2 without provider (backward compat)', () => {
    const capContent = JSON.stringify({
      model: 'claude-sonnet-4-6',
      tools: [
        { ServerName: 'srv1', ToolName: 'fs.read', Role: 'tool', Approval: 'none', Timeout: 0, OnTimeout: '' },
      ],
    })
    mockLoaded(makeRun(), [
      makeStep({ id: 'snap', type: 'capability_snapshot', content: capContent }),
    ])
    renderPage()
    const summary = screen.getByText(/Capability snapshot/)
    expect(summary.textContent).toContain('claude-sonnet-4-6')
    // provider omitted — should not appear in summary
    expect(summary.textContent).not.toContain('anthropic')
  })

  it('renders approval_request placeholder', () => {
    mockLoaded(makeRun(), [
      makeStep({
        id: 's6',
        type: 'approval_request',
        content: JSON.stringify({ tool: 'fs.delete', input: {} }),
      }),
    ])
    renderPage()
    expect(screen.getByText('Approval requested')).toBeInTheDocument()
  })
})

describe('RunDetailPage — filter chips', () => {
  const steps: ApiRunStep[] = [
    makeStep({ id: 's1', type: 'thought', content: JSON.stringify({ text: 'Thought A' }) }),
    makeStep({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'Thought B' }) }),
    makeStep({ id: 's3', step_number: 2, type: 'tool_call', content: JSON.stringify({ tool_name: 'x', server_id: 'srv', input: {} }) }),
    makeStep({ id: 's4', step_number: 3, type: 'error', content: JSON.stringify({ message: 'err', code: 'E' }) }),
    makeStep({ id: 's5', step_number: 4, type: 'tool_result', content: JSON.stringify({ tool_name: 'x', output: '"ok"', is_error: false }) }),
  ]

  beforeEach(() => {
    mockLoaded(makeRun(), steps)
  })

  it('shows all non-snapshot steps on "All" filter', () => {
    renderPage()
    // 2 thoughts + 1 tool_call + 1 error + 1 tool_result = 5 visible
    expect(screen.getByText('Thought A')).toBeInTheDocument()
    expect(screen.getByText('Thought B')).toBeInTheDocument()
  })

  it('shows only thought steps after clicking Thoughts chip', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /thoughts/i }))
    await waitFor(() => {
      expect(screen.getByText('Thought A')).toBeInTheDocument()
      expect(screen.getByText('Thought B')).toBeInTheDocument()
      // tool_call and error should be gone
      expect(screen.queryByText('tool call')).not.toBeInTheDocument()
      expect(screen.queryByText('err')).not.toBeInTheDocument()
    })
  })

  it('shows only tool_call steps after clicking Calls chip', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /^calls/i }))
    await waitFor(() => {
      expect(screen.getByText('tool call')).toBeInTheDocument()
      expect(screen.queryByText('Thought A')).not.toBeInTheDocument()
      expect(screen.queryByText('err')).not.toBeInTheDocument()
    })
  })

  it('shows only tool_result steps after clicking Results chip', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /^results/i }))
    await waitFor(() => {
      expect(screen.getByText('result')).toBeInTheDocument()
      expect(screen.queryByText('Thought A')).not.toBeInTheDocument()
      expect(screen.queryByText('err')).not.toBeInTheDocument()
    })
  })

  it('shows count badge on filter chips', () => {
    renderPage()
    // "Thoughts" chip should show count 2
    const thoughtsChip = screen.getByRole('button', { name: /thoughts/i })
    expect(thoughtsChip.textContent).toContain('2')
  })

  it('shows count 1 for Errors chip', () => {
    renderPage()
    const errorsChip = screen.getByRole('button', { name: /errors/i })
    expect(errorsChip.textContent).toContain('1')
  })

  it('shows count 1 for Calls chip', () => {
    renderPage()
    const callsChip = screen.getByRole('button', { name: /^calls/i })
    expect(callsChip.textContent).toContain('1')
  })

  it('shows count 1 for Results chip', () => {
    renderPage()
    const resultsChip = screen.getByRole('button', { name: /^results/i })
    expect(resultsChip.textContent).toContain('1')
  })

  it('clicking All chip after a filter shows all steps again', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /thoughts/i }))
    await waitFor(() => {
      expect(screen.queryByText('err')).not.toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole('button', { name: /^all/i }))
    await waitFor(() => {
      expect(screen.getByText('Thought A')).toBeInTheDocument()
      expect(screen.getByText('err')).toBeInTheDocument()
    })
  })
})

describe('CollapsibleJSON — isolated', () => {
  it('shows truncated content when collapsed', () => {
    const bigObj = { a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7 }
    render(<CollapsibleJSON value={bigObj} defaultCollapsed={true} />)
    // Should show "more lines" text
    expect(screen.getByText(/more line/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /show all/i })).toBeInTheDocument()
  })

  it('shows full content when expanded', async () => {
    const bigObj = { a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7 }
    render(<CollapsibleJSON value={bigObj} defaultCollapsed={true} />)
    fireEvent.click(screen.getByRole('button', { name: /show all/i }))
    await waitFor(() => {
      expect(screen.queryByText(/more line/i)).not.toBeInTheDocument()
      expect(screen.getByRole('button', { name: /collapse/i })).toBeInTheDocument()
    })
  })

  it('does not show toggle for short objects', () => {
    render(<CollapsibleJSON value={{ a: 1 }} />)
    expect(screen.queryByRole('button', { name: /show all/i })).not.toBeInTheDocument()
  })
})

describe('CopyBlock', () => {
  let originalClipboard: Clipboard

  beforeEach(() => {
    originalClipboard = navigator.clipboard
  })

  afterEach(() => {
    Object.assign(navigator, { clipboard: originalClipboard })
  })

  it('calls clipboard.writeText on copy click', async () => {
    const writeMock = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText: writeMock } })

    render(
      <CopyBlock text="hello world">
        <pre>hello world</pre>
      </CopyBlock>,
    )

    const btn = screen.getByRole('button', { name: /copy/i })
    await act(async () => {
      fireEvent.click(btn)
    })

    expect(writeMock).toHaveBeenCalledWith('hello world')
  })

  it('shows checkmark after copy, then resets after 1800ms', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const writeMock = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText: writeMock } })

    render(
      <CopyBlock text="abc">
        <pre>abc</pre>
      </CopyBlock>,
    )

    const btn = screen.getByRole('button', { name: /copy/i })
    // Click and let the promise resolve
    fireEvent.click(btn)
    // Flush promise microtasks
    await act(async () => {
      await Promise.resolve()
    })

    expect(screen.getByRole('button').textContent).toContain('✓')

    // Advance timers past the 1800ms reset delay
    await act(async () => {
      vi.advanceTimersByTime(1800)
    })

    expect(screen.getByRole('button').textContent).toContain('Copy')

    vi.useRealTimers()
  })
})

describe('RunDetailPage — Load more', () => {
  it('shows Load more button when there are more than 50 steps', () => {
    const steps: ApiRunStep[] = Array.from({ length: 55 }, (_, i) =>
      makeStep({
        id: `s${i}`,
        step_number: i,
        type: 'thought',
        content: JSON.stringify({ text: `Thought ${i}` }),
      }),
    )
    mockLoaded(makeRun(), steps)
    renderPage()
    expect(screen.getByRole('button', { name: /load more/i })).toBeInTheDocument()
  })

  it('does not show Load more button when there are fewer than 50 steps', () => {
    const steps: ApiRunStep[] = Array.from({ length: 10 }, (_, i) =>
      makeStep({
        id: `s${i}`,
        step_number: i,
        type: 'thought',
        content: JSON.stringify({ text: `Thought ${i}` }),
      }),
    )
    mockLoaded(makeRun(), steps)
    renderPage()
    expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument()
  })

  it('loads more steps when button is clicked', async () => {
    const steps: ApiRunStep[] = Array.from({ length: 55 }, (_, i) =>
      makeStep({
        id: `s${i}`,
        step_number: i,
        type: 'thought',
        content: JSON.stringify({ text: `Thought ${i}` }),
      }),
    )
    mockLoaded(makeRun(), steps)
    renderPage()

    const loadMoreBtn = screen.getByRole('button', { name: /load more/i })
    fireEvent.click(loadMoreBtn)

    await waitFor(() => {
      // After loading more, all 55 steps visible, so button gone
      expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument()
    })
  })
})

describe('RunDetailPage — pagination with active filter', () => {
  it('shows Load More when >50 steps match the active filter and loads remaining on click', async () => {
    // 55 tool_call steps + 10 thought steps
    const steps: ApiRunStep[] = [
      ...Array.from({ length: 55 }, (_, i) =>
        makeStep({
          id: `tc${i}`,
          step_number: i,
          type: 'tool_call',
          content: JSON.stringify({ tool_name: `tool_${i}`, server_id: 'srv', input: {} }),
        }),
      ),
      ...Array.from({ length: 10 }, (_, i) =>
        makeStep({
          id: `th${i}`,
          step_number: 55 + i,
          type: 'thought',
          content: JSON.stringify({ text: `Thought ${i}` }),
        }),
      ),
    ]
    mockLoaded(makeRun(), steps)
    renderPage()

    // Filter to only Calls
    fireEvent.click(screen.getByRole('button', { name: /^calls/i }))

    // With 55 tool_call steps and PAGE_SIZE=50, Load More should be visible
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /load more/i })).toBeInTheDocument()
    })

    // Click Load More
    fireEvent.click(screen.getByRole('button', { name: /load more/i }))

    // All 55 tool_call steps now visible — no more Load More button
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument()
    })
  })
})

describe('RunDetailPage — filter does NOT reset displayedCount', () => {
  it('persists displayedCount when switching filters', async () => {
    // 55 thought steps + 55 tool_call steps
    const steps: ApiRunStep[] = [
      ...Array.from({ length: 55 }, (_, i) =>
        makeStep({
          id: `th${i}`,
          step_number: i,
          type: 'thought',
          content: JSON.stringify({ text: `Thought ${i}` }),
        }),
      ),
      ...Array.from({ length: 55 }, (_, i) =>
        makeStep({
          id: `tc${i}`,
          step_number: 55 + i,
          type: 'tool_call',
          content: JSON.stringify({ tool_name: `tool_${i}`, server_id: 'srv', input: {} }),
        }),
      ),
    ]
    mockLoaded(makeRun(), steps)
    renderPage()

    // Start on Thoughts filter — 55 thoughts, 50 shown, Load More visible
    fireEvent.click(screen.getByRole('button', { name: /thoughts/i }))
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /load more/i })).toBeInTheDocument()
    })

    // Load more (displayedCount becomes 100)
    fireEvent.click(screen.getByRole('button', { name: /load more/i }))
    await waitFor(() => {
      // All 55 thoughts shown, no Load More
      expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument()
    })

    // Switch to Calls filter — displayedCount is still 100, which is > 55 tool_calls,
    // so all 55 calls should be shown immediately with no Load More
    fireEvent.click(screen.getByRole('button', { name: /^calls/i }))
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument()
    })
  })
})

describe('RunDetailPage — error box', () => {
  it('shows error box when run status is failed', () => {
    mockLoaded(makeRun({ status: 'failed', error: 'agent timed out' }))
    renderPage()
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText('agent timed out')).toBeInTheDocument()
  })

  it('shows error box when run status is interrupted', () => {
    mockLoaded(makeRun({ status: 'interrupted', error: 'process restarted' }))
    renderPage()
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText('process restarted')).toBeInTheDocument()
  })

  it('does not show error box when run status is running', () => {
    mockLoaded(makeRun({ status: 'running', error: null, completed_at: null }))
    renderPage()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('does not show error box when run is complete', () => {
    mockLoaded(makeRun({ status: 'complete', error: null }))
    renderPage()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

describe('RunDetailPage — "New steps" pill', () => {
  it('shows pill when new steps arrive and user is not near bottom', async () => {
    const initialSteps: ApiRunStep[] = [
      makeStep({ id: 's1', type: 'thought', content: JSON.stringify({ text: 'Initial' }) }),
    ]

    vi.mocked(useRun).mockReturnValue({
      data: makeRun({ status: 'running', completed_at: null }),
      status: 'success',
    } as ReturnType<typeof useRun>)

    vi.mocked(useRunSteps).mockReturnValue({
      data: initialSteps,
      status: 'success',
    } as ReturnType<typeof useRunSteps>)

    // Mock IntersectionObserver with a proper constructor function
    let observerCallback: IntersectionObserverCallback | null = null
    function MockIntersectionObserver(this: IntersectionObserver, cb: IntersectionObserverCallback) {
      observerCallback = cb
      this.observe = (el: Element) => {
        // Fire immediately as NOT intersecting (user scrolled away)
        cb([{ isIntersecting: false, target: el } as IntersectionObserverEntry], this)
      }
      this.unobserve = vi.fn()
      this.disconnect = vi.fn()
    }
    vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

    const qc = makeQueryClient()
    const { rerender } = render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={['/runs/r1']}>
          <Routes>
            <Route path="/runs/:id" element={<RunDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )

    // Simulate new steps arriving by updating the mock return value
    const newSteps: ApiRunStep[] = [
      ...initialSteps,
      makeStep({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'New step' }) }),
    ]

    vi.mocked(useRunSteps).mockReturnValue({
      data: newSteps,
      status: 'success',
    } as ReturnType<typeof useRunSteps>)

    rerender(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={['/runs/r1']}>
          <Routes>
            <Route path="/runs/:id" element={<RunDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /new steps/i })).toBeInTheDocument()
    })

    // Suppress unused variable warning
    void observerCallback

    vi.unstubAllGlobals()
  })

  it('clicking the pill hides it', async () => {
    const initialSteps: ApiRunStep[] = [
      makeStep({ id: 's1', type: 'thought', content: JSON.stringify({ text: 'Initial' }) }),
    ]

    vi.mocked(useRun).mockReturnValue({
      data: makeRun({ status: 'running', completed_at: null }),
      status: 'success',
    } as ReturnType<typeof useRun>)

    vi.mocked(useRunSteps).mockReturnValue({
      data: initialSteps,
      status: 'success',
    } as ReturnType<typeof useRunSteps>)

    // scrollIntoView is not implemented in jsdom; stub it to avoid TypeError
    window.HTMLElement.prototype.scrollIntoView = vi.fn()

    function MockIntersectionObserver(this: IntersectionObserver, cb: IntersectionObserverCallback) {
      this.observe = (el: Element) => {
        cb([{ isIntersecting: false, target: el } as IntersectionObserverEntry], this)
      }
      this.unobserve = vi.fn()
      this.disconnect = vi.fn()
    }
    vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

    const qc = makeQueryClient()
    const { rerender } = render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={['/runs/r1']}>
          <Routes>
            <Route path="/runs/:id" element={<RunDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )

    const newSteps: ApiRunStep[] = [
      ...initialSteps,
      makeStep({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'New step' }) }),
    ]

    vi.mocked(useRunSteps).mockReturnValue({
      data: newSteps,
      status: 'success',
    } as ReturnType<typeof useRunSteps>)

    rerender(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={['/runs/r1']}>
          <Routes>
            <Route path="/runs/:id" element={<RunDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /new steps/i })).toBeInTheDocument()
    })

    fireEvent.click(screen.getByRole('button', { name: /new steps/i }))

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /new steps/i })).not.toBeInTheDocument()
    })

    vi.unstubAllGlobals()
  })
})

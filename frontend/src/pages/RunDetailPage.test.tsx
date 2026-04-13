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

vi.mock('@/hooks/queries/runs')

import { useRun } from '@/hooks/queries/runs'
import { useRunSteps } from '@/hooks/queries/runs'

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
    model: '',
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

function mockError() {
  vi.mocked(useRun).mockReturnValue({
    data: undefined,
    status: 'error',
    error: new Error('Not Found'),
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof useRun>)

  vi.mocked(useRunSteps).mockReturnValue({
    data: undefined,
    status: 'error',
    error: new Error('Not Found'),
  } as unknown as ReturnType<typeof useRunSteps>)
}

function mockSuccessNoData() {
  vi.mocked(useRun).mockReturnValue({
    data: undefined,
    status: 'success',
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof useRun>)

  vi.mocked(useRunSteps).mockReturnValue({
    data: [],
    status: 'success',
  } as unknown as ReturnType<typeof useRunSteps>)
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

  it('renders skeleton blocks and hides run header while pending', () => {
    renderPage()
    const skeletons = document.querySelectorAll('[aria-hidden="true"]')
    expect(skeletons.length).toBeGreaterThan(0)
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

  it('renders tool_call step with tool name via ToolBlock', () => {
    mockLoaded(makeRun(), [
      makeStep({
        id: 's2',
        type: 'tool_call',
        content: JSON.stringify({ tool_name: 'fs.read', server_id: 'srv1', input: { path: '/tmp' } }),
      }),
    ])
    renderPage()
    // ToolBlock renders the tool name in monospace bold in the header
    expect(screen.getByText('fs.read')).toBeInTheDocument()
    // ToolBlock renders the server_id as a pill
    expect(screen.getByText('srv1')).toBeInTheDocument()
  })

  it('renders tool_call step when capability_snapshot marks it as tool', () => {
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
    expect(screen.getByText('fs.write')).toBeInTheDocument()
  })

  it('renders tool_result step', () => {
    // An orphan tool_result (no preceding tool_call) falls back to CollapsibleJSON rendering.
    // The JSON content is visible as a pre/code block — check for a key value in the output.
    mockLoaded(makeRun(), [
      makeStep({
        id: 's3',
        type: 'tool_result',
        content: JSON.stringify({ tool_name: 'fs.read', output: '"file contents"', is_error: false }),
      }),
    ])
    renderPage()
    // The tool_name value appears inside the JSON pre block
    expect(screen.getByText(/fs\.read/)).toBeInTheDocument()
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
    // CompleteBlock renders "Run complete" as its label (message content is not shown)
    expect(screen.getByText('Run complete')).toBeInTheDocument()
  })

  it('renders capability_snapshot info in header bar (collapsed by default)', () => {
    const capContent = JSON.stringify([
      { ServerName: 'srv1', ToolName: 'fs.read', Role: 'tool', Approval: 'none', Timeout: 0, OnTimeout: '' },
    ])
    mockLoaded(makeRun(), [
      makeStep({ id: 'snap', type: 'capability_snapshot', content: capContent }),
    ])
    renderPage()
    // Capability info is shown in header bar, not as a timeline card
    expect(screen.getByText(/1 tool/)).toBeInTheDocument()
    // Table should not be visible by default
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('expands capability bar to show tool table on click', async () => {
    const capContent = JSON.stringify([
      { ServerName: 'srv1', ToolName: 'fs.read', Role: 'tool', Approval: 'none', Timeout: 0, OnTimeout: '' },
    ])
    mockLoaded(makeRun(), [
      makeStep({ id: 'snap', type: 'capability_snapshot', content: capContent }),
    ])
    renderPage()
    fireEvent.click(screen.getByText(/1 tool/))
    await waitFor(() => {
      expect(screen.getByRole('table')).toBeInTheDocument()
    })
  })

  it('renders capability_snapshot V2 with provider in header bar', () => {
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
    const bar = screen.getByText(/1 tool/)
    expect(bar.textContent).toContain('anthropic')
    expect(bar.textContent).toContain('claude-sonnet-4-6')
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
    const bar = screen.getByText(/1 tool/)
    expect(bar.textContent).toContain('claude-sonnet-4-6')
    // provider omitted — should not appear
    expect(bar.textContent).not.toContain('anthropic')
  })

  it('renders approval_request as a ToolBlock (denied state when no tool_call follows)', () => {
    mockLoaded(makeRun(), [
      makeStep({
        id: 's6',
        type: 'approval_request',
        content: JSON.stringify({ tool: 'fs.delete', input: {} }),
      }),
    ])
    renderPage()
    // ToolBlock renders the tool name from the approval_request
    expect(screen.getByText('fs.delete')).toBeInTheDocument()
    // Denied state shows "Denied" in header pill and pane text
    const deniedLabels = screen.getAllByText('Denied')
    expect(deniedLabels.length).toBeGreaterThanOrEqual(1)
  })
})

describe('RunDetailPage — filter chips', () => {
  // After pairToolBlocks this fixture produces 7 visual blocks:
  //   thought A, thought B, ToolBlock(x, ok), error, thinking, ToolBlock(y, error), ToolBlock(approval z denied)
  const steps: ApiRunStep[] = [
    makeStep({ id: 's1', type: 'thought', content: JSON.stringify({ text: 'Thought A' }) }),
    makeStep({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'Thought B' }) }),
    makeStep({ id: 's3', step_number: 2, type: 'tool_call', content: JSON.stringify({ tool_name: 'x', server_id: 'srv', input: {} }) }),
    makeStep({ id: 's4', step_number: 3, type: 'tool_result', content: JSON.stringify({ tool_name: 'x', output: '"ok"', is_error: false }) }),
    makeStep({ id: 's5', step_number: 4, type: 'error', content: JSON.stringify({ message: 'err', code: 'E' }) }),
    makeStep({ id: 's6', step_number: 5, type: 'thinking', content: JSON.stringify({ text: 'Deep thinking', redacted: false }) }),
    makeStep({ id: 's7', step_number: 6, type: 'tool_call', content: JSON.stringify({ tool_name: 'y', server_id: 'srv', input: {} }) }),
    makeStep({ id: 's8', step_number: 7, type: 'tool_result', content: JSON.stringify({ tool_name: 'y', output: '"fail"', is_error: true }) }),
    makeStep({ id: 's9', step_number: 8, type: 'approval_request', content: JSON.stringify({ tool: 'z', input: {} }) }),
  ]

  beforeEach(() => {
    mockLoaded(makeRun(), steps)
  })

  it('shows all non-snapshot steps on "All" filter', () => {
    renderPage()
    // 7 visual blocks: thought A, thought B, ToolBlock(x), error, thinking, ToolBlock(y), ToolBlock(z)
    expect(screen.getByText('Thought A')).toBeInTheDocument()
    expect(screen.getByText('Thought B')).toBeInTheDocument()
  })

  it('shows only thought steps after clicking Thoughts chip', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /thoughts/i }))
    await waitFor(() => {
      expect(screen.getByText('Thought A')).toBeInTheDocument()
      expect(screen.getByText('Thought B')).toBeInTheDocument()
      // tool blocks and error should be gone
      expect(screen.queryByText('tool call')).not.toBeInTheDocument()
      expect(screen.queryByText('err')).not.toBeInTheDocument()
    })
  })

  it('shows only tool steps (ToolBlock) after clicking Tools chip', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /^tools/i }))
    await waitFor(() => {
      // All 3 tool blocks should be visible: x (ok), y (error), z (approval denied)
      expect(screen.getByText('x')).toBeInTheDocument()
      expect(screen.getByText('y')).toBeInTheDocument()
      expect(screen.getByText('z')).toBeInTheDocument()
      // Non-tool items hidden
      expect(screen.queryByText('Thought A')).not.toBeInTheDocument()
      expect(screen.queryByText('err')).not.toBeInTheDocument()
      expect(screen.queryByText('Deep thinking')).not.toBeInTheDocument()
    })
  })

  it('shows error steps and tool blocks with is_error after clicking Errors chip', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /errors/i }))
    await waitFor(() => {
      // Standalone error step visible
      expect(screen.getByText('err')).toBeInTheDocument()
      // Tool block y has is_error result — visible
      expect(screen.getByText('y')).toBeInTheDocument()
      // Thoughts, thinking, non-error tool blocks hidden
      expect(screen.queryByText('Thought A')).not.toBeInTheDocument()
      expect(screen.queryByText('Deep thinking')).not.toBeInTheDocument()
      expect(screen.queryByText('x')).not.toBeInTheDocument()
    })
  })

  it('shows only thinking steps after clicking Thinking chip', async () => {
    renderPage()
    // Use getAllByRole because ThinkingBlock also renders a div[role="button"] with
    // "Thinking" in its label. We want the <button> chip, which is the first match.
    const thinkingButtons = screen.getAllByRole('button', { name: /^thinking/i })
    const thinkingChip = thinkingButtons.find((el) => el.tagName === 'BUTTON')!
    fireEvent.click(thinkingChip)
    await waitFor(() => {
      // ThinkingBlock renders a collapsible div[role="button"] with label "Thinking"
      // (the content "Deep thinking" is only visible when expanded).
      // Verify the ThinkingBlock is present by checking its collapsed header is rendered.
      const rendered = screen.getAllByRole('button', { name: /^thinking/i })
      const thinkingBlock = rendered.find((el) => el.tagName !== 'BUTTON')
      expect(thinkingBlock).toBeInTheDocument()
      // Other step types hidden
      expect(screen.queryByText('Thought A')).not.toBeInTheDocument()
      expect(screen.queryByText('err')).not.toBeInTheDocument()
      expect(screen.queryByText('x')).not.toBeInTheDocument()
    })
  })

  it('shows approval blocks after clicking Approvals chip', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /approvals/i }))
    await waitFor(() => {
      // ToolBlock z has a non-null approval — visible
      expect(screen.getByText('z')).toBeInTheDocument()
      // Other blocks hidden
      expect(screen.queryByText('Thought A')).not.toBeInTheDocument()
      expect(screen.queryByText('x')).not.toBeInTheDocument()
      expect(screen.queryByText('y')).not.toBeInTheDocument()
      expect(screen.queryByText('err')).not.toBeInTheDocument()
    })
  })

  it.each([
    [/thoughts/i, '2'],
    [/errors/i, '2'],
    [/^tools/i, '3'],
    [/approvals/i, '1'],
  ])('shows correct count badge for %s chip', (chipPattern, expectedCount) => {
    renderPage()
    const allMatches = screen.getAllByRole('button', { name: chipPattern })
    const chip = allMatches.find((el) => el.tagName === 'BUTTON') ?? allMatches[0]
    expect(chip.textContent).toContain(expectedCount)
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
    // Object must exceed 12 preview lines when pretty-printed (14 keys + braces = 16 lines)
    const bigObj = { a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7, h: 8, i: 9, j: 10, k: 11, l: 12, m: 13, n: 14 }
    render(<CollapsibleJSON value={bigObj} defaultCollapsed={true} />)
    // Should show "more lines" text
    expect(screen.getByText(/more line/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /show all/i })).toBeInTheDocument()
  })

  it('shows full content when expanded', async () => {
    const bigObj = { a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7, h: 8, i: 9, j: 10, k: 11, l: 12, m: 13, n: 14 }
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

    expect(screen.getByRole('button').textContent).toContain('Copied')

    // Advance timers past the 1800ms reset delay
    await act(async () => {
      vi.advanceTimersByTime(1800)
    })

    expect(screen.getByRole('button').textContent?.trim()).toBe('Copy')

    vi.useRealTimers()
  })
})

describe('RunDetailPage — Load more', () => {
  it.each([
    [55, true],
    [10, false],
  ])('with %i steps, Load more button present=%s', (count, shouldExist) => {
    const steps: ApiRunStep[] = Array.from({ length: count }, (_, i) =>
      makeStep({
        id: `s${i}`,
        step_number: i,
        type: 'thought',
        content: JSON.stringify({ text: `Thought ${i}` }),
      }),
    )
    mockLoaded(makeRun(), steps)
    renderPage()
    if (shouldExist) {
      expect(screen.getByRole('button', { name: /load more/i })).toBeInTheDocument()
    } else {
      expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument()
    }
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

    // Filter to only Tools
    fireEvent.click(screen.getByRole('button', { name: /^tools/i }))

    // With 55 tool_call steps and PAGE_SIZE=50, Load More should be visible
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /load more/i })).toBeInTheDocument()
    }, { timeout: 10000 })

    // Click Load More
    fireEvent.click(screen.getByRole('button', { name: /load more/i }))

    // All 55 tool_call steps now visible — no more Load More button
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument()
    }, { timeout: 10000 })
  }, 15000)
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

    // Switch to Tools filter — displayedCount is still 100, which is > 55 tool_calls,
    // so all 55 tool blocks should be shown immediately with no Load More
    fireEvent.click(screen.getByRole('button', { name: /^tools/i }))
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument()
    })
  })
})

describe('RunDetailPage — error box', () => {
  it.each([
    ['failed', 'agent timed out'],
    ['interrupted', 'process restarted'],
  ] as const)('shows error box when run status is %s', (status, errorMsg) => {
    mockLoaded(makeRun({ status, error: errorMsg }))
    renderPage()
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText(errorMsg)).toBeInTheDocument()
  })

  it.each([
    ['running', { completed_at: null }],
    ['complete', {}],
  ] as const)('does not show error box when run status is %s', (status, overrides) => {
    mockLoaded(makeRun({ status, error: null, ...overrides }))
    renderPage()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows a copy button in the error box for a failed run', async () => {
    const errorMsg = 'context deadline exceeded'
    const writeMock = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText: writeMock } })

    mockLoaded(makeRun({ status: 'failed', error: errorMsg }))
    renderPage()

    const copyBtn = screen.getByRole('button', { name: /copy/i })
    await act(async () => {
      fireEvent.click(copyBtn)
    })

    expect(writeMock).toHaveBeenCalledWith(errorMsg)
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

describe('RunDetailPage — error state (404)', () => {
  beforeEach(() => {
    mockError()
  })

  it('shows error message when run fetch fails', () => {
    renderPage()
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText(/failed to load run/i)).toBeInTheDocument()
  })

  it('shows Retry button when run fetch fails', () => {
    renderPage()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })

  it('does not render run header when in error state', () => {
    renderPage()
    expect(screen.queryByText(/back/i)).not.toBeInTheDocument()
  })
})

describe('RunDetailPage — not found empty state', () => {
  beforeEach(() => {
    mockSuccessNoData()
  })

  it('shows "Run not found" when run data is undefined after success', () => {
    renderPage()
    expect(screen.getByText('Run not found')).toBeInTheDocument()
  })

  it('shows a link back to runs list', () => {
    renderPage()
    const link = screen.getByRole('link', { name: /back to runs/i })
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', '/runs')
  })
})

describe('RunDetailPage — live duration counter', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('duration ticks while run is running', async () => {
    vi.useFakeTimers()
    // Set the fake clock before building the run so started_at is relative
    // to a known reference point and not affected by the fake clock itself.
    vi.setSystemTime(new Date('2025-01-01T12:01:00Z'))

    const run = makeRun({
      status: 'running',
      completed_at: null,
      started_at: '2025-01-01T12:00:00Z', // 60 seconds before fake now
    })
    mockLoaded(run)
    renderPage()

    // Initial duration should be 60 seconds
    expect(screen.getByText('1m 0s')).toBeInTheDocument()

    // After 5 more seconds the displayed duration should update
    await act(async () => {
      vi.advanceTimersByTime(5000)
    })

    expect(screen.getByText('1m 5s')).toBeInTheDocument()
  })

  it('duration is static for a completed run', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2025-01-01T12:01:00Z'))

    const run = makeRun({
      status: 'complete',
      started_at: '2025-01-01T12:00:00Z',
      completed_at: '2025-01-01T12:01:00Z', // exactly 60 seconds
    })
    mockLoaded(run)
    renderPage()

    expect(screen.getByText('1m 0s')).toBeInTheDocument()

    // Advancing the clock should not change the displayed duration
    await act(async () => {
      vi.advanceTimersByTime(5000)
    })

    expect(screen.getByText('1m 0s')).toBeInTheDocument()
  })
})

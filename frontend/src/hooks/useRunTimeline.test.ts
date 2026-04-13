import { describe, it, expect } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useRunTimeline } from './useRunTimeline'
import type { ApiRunStep } from '@/api/types'

function makeStep(overrides?: Partial<ApiRunStep>): ApiRunStep {
  return {
    id: 's1',
    run_id: 'r1',
    step_number: 0,
    type: 'thought',
    content: JSON.stringify({ text: 'thinking' }),
    token_cost: 10,
    created_at: new Date().toISOString(),
    ...overrides,
  }
}

describe('useRunTimeline — initial state', () => {
  it('returns empty timeline for no steps', () => {
    const { result } = renderHook(() => useRunTimeline([], 'all'))
    expect(result.current.timelineItems).toHaveLength(0)
    expect(result.current.filteredItems).toHaveLength(0)
    expect(result.current.hasMore).toBe(false)
    expect(result.current.remainingCount).toBe(0)
  })

  it('returns all items under the "all" filter', () => {
    const steps: ApiRunStep[] = [
      makeStep({ id: 's1', type: 'thought', content: JSON.stringify({ text: 'A' }) }),
      makeStep({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'B' }) }),
    ]
    const { result } = renderHook(() => useRunTimeline(steps, 'all'))
    // 2 non-snapshot steps → 2 paired items → 2 timeline items
    expect(result.current.timelineItems).toHaveLength(2)
    expect(result.current.hasMore).toBe(false)
  })
})

describe('useRunTimeline — snapshot separation', () => {
  it('separates capability_snapshot into snapshotSteps, not timelineItems', () => {
    const steps: ApiRunStep[] = [
      makeStep({ id: 's1', type: 'thought', content: JSON.stringify({ text: 'A' }) }),
      makeStep({
        id: 'snap',
        step_number: 1,
        type: 'capability_snapshot',
        content: JSON.stringify([
          { server_name: 'srv', tool_name: 'fs.read', approval: 'none', timeout: 0, on_timeout: '' },
        ]),
      }),
    ]
    const { result } = renderHook(() => useRunTimeline(steps, 'all'))
    // snapshot is in snapshotSteps, not in timelineItems (rendered in header instead)
    expect(result.current.snapshotSteps).toHaveLength(1)
    expect(result.current.timelineItems).toHaveLength(1)
    expect(result.current.timelineItems[0]).toMatchObject({ type: 'thought' })
    // snapshot does not count in filteredItems
    expect(result.current.filteredItems).toHaveLength(1)
  })

  it('snapshot steps are excluded from filter counts', () => {
    const steps: ApiRunStep[] = [
      makeStep({
        id: 'snap',
        type: 'capability_snapshot',
        content: JSON.stringify([]),
      }),
    ]
    const { result } = renderHook(() => useRunTimeline(steps, 'all'))
    expect(result.current.counts.all).toBe(0)
  })
})

describe('useRunTimeline — filter counts', () => {
  // 9 steps that pair into 7 visual blocks:
  //   thought, thought, ToolBlock(x ok), error, thinking, ToolBlock(y error), ToolBlock(z approval)
  const steps: ApiRunStep[] = [
    makeStep({ id: 's1', type: 'thought', content: JSON.stringify({ text: 'T1' }) }),
    makeStep({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'T2' }) }),
    makeStep({ id: 's3', step_number: 2, type: 'tool_call', content: JSON.stringify({ tool_name: 'x', server_id: 'srv', input: {} }) }),
    makeStep({ id: 's4', step_number: 3, type: 'tool_result', content: JSON.stringify({ tool_name: 'x', output: '"ok"', is_error: false }) }),
    makeStep({ id: 's5', step_number: 4, type: 'error', content: JSON.stringify({ message: 'err', code: 'E' }) }),
    makeStep({ id: 's6', step_number: 5, type: 'thinking', content: JSON.stringify({ text: 'deep', redacted: false }) }),
    makeStep({ id: 's7', step_number: 6, type: 'tool_call', content: JSON.stringify({ tool_name: 'y', server_id: 'srv', input: {} }) }),
    makeStep({ id: 's8', step_number: 7, type: 'tool_result', content: JSON.stringify({ tool_name: 'y', output: '"fail"', is_error: true }) }),
    makeStep({ id: 's9', step_number: 8, type: 'approval_request', content: JSON.stringify({ tool: 'z', input: {} }) }),
  ]

  it('counts all visual blocks correctly', () => {
    const { result } = renderHook(() => useRunTimeline(steps, 'all'))
    expect(result.current.counts.all).toBe(7)
  })

  it('counts tool blocks (3 total: x, y, z)', () => {
    const { result } = renderHook(() => useRunTimeline(steps, 'all'))
    expect(result.current.counts.tool).toBe(3)
  })

  it('counts thought steps (2)', () => {
    const { result } = renderHook(() => useRunTimeline(steps, 'all'))
    expect(result.current.counts.thought).toBe(2)
  })

  it('counts thinking steps (1)', () => {
    const { result } = renderHook(() => useRunTimeline(steps, 'all'))
    expect(result.current.counts.thinking).toBe(1)
  })

  it('counts errors (1 standalone + 1 tool block with is_error = 2)', () => {
    const { result } = renderHook(() => useRunTimeline(steps, 'all'))
    expect(result.current.counts.error).toBe(2)
  })

  it('counts approvals (1 approval_request block)', () => {
    const { result } = renderHook(() => useRunTimeline(steps, 'all'))
    expect(result.current.counts.approval).toBe(1)
  })
})

describe('useRunTimeline — filtering', () => {
  const steps: ApiRunStep[] = [
    makeStep({ id: 's1', type: 'thought', content: JSON.stringify({ text: 'T1' }) }),
    makeStep({ id: 's2', step_number: 1, type: 'tool_call', content: JSON.stringify({ tool_name: 'x', server_id: 'srv', input: {} }) }),
    makeStep({ id: 's3', step_number: 2, type: 'tool_result', content: JSON.stringify({ tool_name: 'x', output: '"ok"', is_error: false }) }),
  ]

  it('filter "thought" returns only thought steps', () => {
    const { result } = renderHook(() => useRunTimeline(steps, 'thought'))
    expect(result.current.filteredItems).toHaveLength(1)
    expect(result.current.filteredItems[0]).toMatchObject({ type: 'thought' })
  })

  it('filter "tool" returns only tool blocks', () => {
    const { result } = renderHook(() => useRunTimeline(steps, 'tool'))
    expect(result.current.filteredItems).toHaveLength(1)
    // ToolBlockData has 'call', not 'type'
    expect(result.current.filteredItems[0]).toHaveProperty('call')
  })

  it('filter "error" returns only error items', () => {
    const errorSteps: ApiRunStep[] = [
      makeStep({ id: 's1', type: 'error', content: JSON.stringify({ message: 'boom', code: 'E' }) }),
      makeStep({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'fine' }) }),
    ]
    const { result } = renderHook(() => useRunTimeline(errorSteps, 'error'))
    expect(result.current.filteredItems).toHaveLength(1)
    expect(result.current.filteredItems[0]).toMatchObject({ type: 'error' })
  })

  it('filter "thinking" returns only thinking steps', () => {
    const thinkingSteps: ApiRunStep[] = [
      makeStep({ id: 's1', type: 'thinking', content: JSON.stringify({ text: 'deep', redacted: false }) }),
      makeStep({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'T' }) }),
    ]
    const { result } = renderHook(() => useRunTimeline(thinkingSteps, 'thinking'))
    expect(result.current.filteredItems).toHaveLength(1)
    expect(result.current.filteredItems[0]).toMatchObject({ type: 'thinking' })
  })

  it('filter "approval" returns approval blocks', () => {
    const approvalSteps: ApiRunStep[] = [
      makeStep({ id: 's1', type: 'approval_request', content: JSON.stringify({ tool: 'z', input: {} }) }),
      makeStep({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'T' }) }),
    ]
    const { result } = renderHook(() => useRunTimeline(approvalSteps, 'approval'))
    expect(result.current.filteredItems).toHaveLength(1)
    expect(result.current.filteredItems[0]).toHaveProperty('approval')
  })
})

describe('useRunTimeline — pagination', () => {
  const manySteps: ApiRunStep[] = Array.from({ length: 55 }, (_, i) =>
    makeStep({
      id: `s${i}`,
      step_number: i,
      type: 'thought',
      content: JSON.stringify({ text: `T${i}` }),
    }),
  )

  it('shows only first 50 items initially', () => {
    const { result } = renderHook(() => useRunTimeline(manySteps, 'all'))
    // 50 paginated + 0 snapshots
    expect(result.current.timelineItems).toHaveLength(50)
    expect(result.current.hasMore).toBe(true)
    expect(result.current.remainingCount).toBe(5)
  })

  it('loadMore shows all 55 items', () => {
    const { result } = renderHook(() => useRunTimeline(manySteps, 'all'))
    act(() => { result.current.loadMore() })
    expect(result.current.timelineItems).toHaveLength(55)
    expect(result.current.hasMore).toBe(false)
    expect(result.current.remainingCount).toBe(0)
  })

  it('displayedCount is shared across filter switches (does not reset)', () => {
    // 55 thoughts + 10 tool_calls
    const mixed: ApiRunStep[] = [
      ...Array.from({ length: 55 }, (_, i) =>
        makeStep({ id: `th${i}`, step_number: i, type: 'thought', content: JSON.stringify({ text: `T${i}` }) }),
      ),
      ...Array.from({ length: 10 }, (_, i) =>
        makeStep({ id: `tc${i}`, step_number: 55 + i, type: 'tool_call', content: JSON.stringify({ tool_name: `t${i}`, server_id: 'srv', input: {} }) }),
      ),
    ]

    // Use a mutable ref trick: start with 'all', loadMore, then check 'tool' filter
    // renderHook doesn't easily re-call with different args in one shot —
    // use rerender to switch filter
    let filter: Parameters<typeof useRunTimeline>[1] = 'all'
    const { result, rerender } = renderHook(() => useRunTimeline(mixed, filter))

    // Initially 50 shown, load more so displayedCount becomes 100
    act(() => { result.current.loadMore() })
    // Now all 65 items are under limit (100) — all shown with 'all' filter
    expect(result.current.hasMore).toBe(false)

    // Switch to tool filter — displayedCount 100 > 10 tool blocks, so no hasMore
    filter = 'tool'
    rerender()
    expect(result.current.filteredItems).toHaveLength(10)
    expect(result.current.hasMore).toBe(false)
  })
})

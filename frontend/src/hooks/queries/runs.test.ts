import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'
import React from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { useRunSteps, PAGE_SIZE_STEPS } from './runs'
import type { ApiRunStep } from '@/api/types'
import { queryKeys } from '@/hooks/queryKeys'

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return {
    client,
    wrapper({ children }: { children: React.ReactNode }) {
      return React.createElement(QueryClientProvider, { client }, children)
    },
  }
}

function makeStep(stepNumber: number): ApiRunStep {
  return {
    id: `step-${stepNumber}`,
    run_id: 'run1',
    step_number: stepNumber,
    type: 'thought',
    content: JSON.stringify({ text: `step ${stepNumber}` }),
    token_cost: 0,
    created_at: new Date().toISOString(),
  }
}

function makeSteps(count: number, offset = 0): ApiRunStep[] {
  return Array.from({ length: count }, (_, i) => makeStep(offset + i))
}

describe('useRunSteps', () => {
  beforeEach(() => {
    // Reset to empty handlers; each test installs its own.
  })

  it('initial mount fetches ?limit=PAGE_SIZE_STEPS', async () => {
    const steps = makeSteps(3)
    let capturedUrl = ''

    server.use(
      http.get('/api/v1/runs/run1/steps', ({ request }) => {
        capturedUrl = request.url
        return HttpResponse.json({ data: steps })
      }),
    )

    const { wrapper } = makeWrapper()
    const { result } = renderHook(() => useRunSteps('run1'), { wrapper })

    await waitFor(() => {
      expect(result.current.status).toBe('success')
      expect(result.current.steps).toHaveLength(3)
    })

    expect(capturedUrl).toContain(`limit=${PAGE_SIZE_STEPS}`)
    expect(result.current.steps[0].step_number).toBe(0)
  })

  it('loadMore fetches ?after=<last>&limit=PAGE_SIZE_STEPS and appends steps', async () => {
    const firstPage = makeSteps(PAGE_SIZE_STEPS)
    const secondPage = makeSteps(3, PAGE_SIZE_STEPS)
    let callCount = 0

    server.use(
      http.get('/api/v1/runs/run1/steps', ({ request }) => {
        callCount++
        const url = new URL(request.url)
        const after = url.searchParams.get('after')
        if (after === null || after === '-1') {
          return HttpResponse.json({ data: firstPage })
        }
        return HttpResponse.json({ data: secondPage })
      }),
    )

    const { wrapper } = makeWrapper()
    const { result } = renderHook(() => useRunSteps('run1'), { wrapper })

    await waitFor(() => {
      expect(result.current.status).toBe('success')
      expect(result.current.steps).toHaveLength(PAGE_SIZE_STEPS)
    })
    expect(result.current.hasMore).toBe(true)

    await act(async () => {
      await result.current.loadMore()
    })

    expect(result.current.steps).toHaveLength(PAGE_SIZE_STEPS + 3)
    expect(callCount).toBe(2)
  })

  it('hasMore is false when server returns fewer than PAGE_SIZE_STEPS rows', async () => {
    const steps = makeSteps(10)

    server.use(
      http.get('/api/v1/runs/run1/steps', () =>
        HttpResponse.json({ data: steps }),
      ),
    )

    const { wrapper } = makeWrapper()
    const { result } = renderHook(() => useRunSteps('run1'), { wrapper })

    await waitFor(() => {
      expect(result.current.status).toBe('success')
      expect(result.current.steps).toHaveLength(10)
    })

    expect(result.current.hasMore).toBe(false)
  })

  it('extraPages are reset when the baseline query is invalidated', async () => {
    // Use a small initial page so loadMore can use a distinct after-cursor.
    const initialFirstPage = makeSteps(10)
    const extraPage = makeSteps(5, 10)
    let refetchCount = 0

    server.use(
      http.get('/api/v1/runs/run1/steps', ({ request }) => {
        const url = new URL(request.url)
        const after = url.searchParams.get('after')
        // loadMore call: return the extra page
        if (after !== null && after !== '-1') {
          return HttpResponse.json({ data: extraPage })
        }
        refetchCount++
        return HttpResponse.json({ data: initialFirstPage })
      }),
    )

    const { client, wrapper } = makeWrapper()
    const { result } = renderHook(() => useRunSteps('run1'), { wrapper })

    await waitFor(() => {
      expect(result.current.status).toBe('success')
      expect(result.current.steps).toHaveLength(10)
    })
    expect(refetchCount).toBe(1)

    // Load an extra page so extraPages has 5 additional items.
    await act(async () => {
      await result.current.loadMore()
    })

    await waitFor(() => {
      expect(result.current.steps).toHaveLength(15)
    })

    // Invalidate the baseline query — simulates SSE run.step_added or mutation.
    await act(async () => {
      await client.invalidateQueries({ queryKey: queryKeys.runs.steps('run1') })
    })

    // After the refetch triggered by invalidation, extraPages are dropped and
    // only the re-fetched first page is visible.
    await waitFor(() => {
      expect(result.current.steps.length).toBeLessThanOrEqual(10)
      expect(refetchCount).toBeGreaterThan(1)
    })
  })
})

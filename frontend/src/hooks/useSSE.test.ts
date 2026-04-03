import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { sse } from 'msw'
import { server } from '@/test/server'
import { useSSE } from './useSSE'
import { queryKeys } from '@/hooks/queryKeys'

type TestSSEEventMap = {
  'run.status_changed': unknown
  'run.step_added': unknown
  'approval.created': unknown
  'approval.resolved': unknown
  'feedback.created': unknown
  'feedback.resolved': unknown
  'feedback.timed_out': unknown
}

function makeWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client: queryClient }, children)
  }
}

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

describe('useSSE', () => {
  it('transitions to connected when the EventSource opens', async () => {
    server.use(
      sse('/api/v1/events', () => {
        // resolver does nothing — the open event fires automatically
      }),
    )

    const qc = makeQueryClient()
    const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })

    await waitFor(() => {
      expect(result.current.connectionState).toBe('connected')
    })
  })

  it('transitions to reconnecting when the server closes the stream', async () => {
    server.use(
      sse('/api/v1/events', ({ client }) => {
        // Close immediately after the connection is established.
        client.close()
      }),
    )

    const qc = makeQueryClient()
    const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })

    // The EventSource polyfill schedules a reconnect when the server closes the
    // stream — readyState is CONNECTING (not CLOSED) when onerror fires, so the
    // hook correctly reports 'reconnecting' rather than 'disconnected'.
    await waitFor(() => {
      expect(result.current.connectionState).toBe('reconnecting')
    })
  })

  it.each([
    ['run.status_changed', JSON.stringify({ runId: 'r1' }), [queryKeys.runs.all, queryKeys.policies.all]],
    ['run.step_added', JSON.stringify({ run_id: 'r1', step_id: 's1', step_number: 0, type: 'thought' }), [queryKeys.runs.steps('r1')]],
    ['approval.created', JSON.stringify({ approvalId: 'a1' }), [queryKeys.approvals.all]],
    ['approval.resolved', JSON.stringify({ approvalId: 'a1' }), [queryKeys.approvals.all, queryKeys.runs.all]],
    ['feedback.created', JSON.stringify({ run_id: 'r1' }), [queryKeys.runs.all, queryKeys.stats.all, queryKeys.attention.all]],
    ['feedback.resolved', JSON.stringify({ run_id: 'r1' }), [queryKeys.runs.all, queryKeys.runs.detail('r1'), queryKeys.runs.steps('r1'), queryKeys.stats.all, queryKeys.attention.all]],
    ['feedback.timed_out', JSON.stringify({ run_id: 'r1' }), [queryKeys.runs.all, queryKeys.runs.detail('r1'), queryKeys.runs.steps('r1'), queryKeys.stats.all, queryKeys.attention.all]],
  ] as [string, string, unknown[]][])(
    'invalidates correct query keys on %s',
    async (eventType, payload, expectedKeys) => {
      let pushEvent!: (type: string, data: string) => void

      server.use(
        sse<TestSSEEventMap>('/api/v1/events', ({ client }) => {
          pushEvent = (type, data) => client.send({ event: type as keyof TestSSEEventMap, data })
        }),
      )

      const qc = makeQueryClient()
      const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')

      const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })
      await waitFor(() => expect(result.current.connectionState).toBe('connected'))

      act(() => {
        pushEvent(eventType, payload)
      })

      await waitFor(() => {
        const keys = invalidateSpy.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey)
        for (const expected of expectedKeys) {
          expect(keys).toContainEqual(expected)
        }
      })
    },
  )

  it('logs error and does not crash on malformed event data', async () => {
    let pushEvent!: (type: string, data: string) => void

    server.use(
      sse<TestSSEEventMap>('/api/v1/events', ({ client }) => {
        pushEvent = (type, data) => client.send({ event: type as keyof TestSSEEventMap, data })
      }),
    )

    const qc = makeQueryClient()
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.connectionState).toBe('connected'))

    // run.step_added has parseRunId: true, so malformed JSON triggers the catch block
    act(() => {
      pushEvent('run.step_added', 'not valid json')
    })

    await waitFor(() => {
      expect(consoleSpy).toHaveBeenCalledWith(
        expect.stringContaining('useSSE: failed to parse run.step_added payload'),
        'not valid json',
      )
    })

    // Hook should still be connected — not crashed
    expect(result.current.connectionState).toBe('connected')

    consoleSpy.mockRestore()
  })

  it('closes the EventSource when the hook unmounts', async () => {
    const closeSpy = vi.spyOn(globalThis.EventSource.prototype, 'close')

    server.use(
      sse('/api/v1/events', () => {
        // stay open
      }),
    )

    const qc = makeQueryClient()
    const { result, unmount } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.connectionState).toBe('connected'))

    unmount()

    expect(closeSpy).toHaveBeenCalledTimes(1)
  })
})

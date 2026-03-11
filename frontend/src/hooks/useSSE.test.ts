import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { sse } from 'msw'
import { server } from '@/test/server'
import { useSSE } from './useSSE'

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

  it('invalidates runs and policies queries on run.status_changed', async () => {
    let pushEvent!: (type: string, data: string) => void

    server.use(
      sse('/api/v1/events', ({ client }) => {
        pushEvent = (type, data) => client.send({ event: type, data })
      }),
    )

    const qc = makeQueryClient()
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')

    const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.connectionState).toBe('connected'))

    act(() => {
      pushEvent('run.status_changed', JSON.stringify({ runId: 'r1' }))
    })

    await waitFor(() => {
      const keys = invalidateSpy.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey)
      expect(keys).toContainEqual(['runs'])
      expect(keys).toContainEqual(['policies'])
    })
  })

  it('appends a step to the query cache on run.step_added', async () => {
    let pushEvent!: (type: string, data: string) => void

    server.use(
      sse('/api/v1/events', ({ client }) => {
        pushEvent = (type, data) => client.send({ event: type, data })
      }),
    )

    const qc = makeQueryClient()
    const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.connectionState).toBe('connected'))

    act(() => {
      pushEvent('run.step_added', JSON.stringify({ runId: 'r1', step: { id: 's1' } }))
    })

    await waitFor(() => {
      const steps = qc.getQueryData<unknown[]>(['runs', 'r1', 'steps'])
      expect(steps).toEqual([{ id: 's1' }])
    })
  })

  it('invalidates approvals query on approval.created', async () => {
    let pushEvent!: (type: string, data: string) => void

    server.use(
      sse('/api/v1/events', ({ client }) => {
        pushEvent = (type, data) => client.send({ event: type, data })
      }),
    )

    const qc = makeQueryClient()
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')

    const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.connectionState).toBe('connected'))

    act(() => {
      pushEvent('approval.created', JSON.stringify({ approvalId: 'a1' }))
    })

    await waitFor(() => {
      const keys = invalidateSpy.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey)
      expect(keys).toContainEqual(['approvals'])
    })
  })

  it('invalidates approvals and runs queries on approval.resolved', async () => {
    let pushEvent!: (type: string, data: string) => void

    server.use(
      sse('/api/v1/events', ({ client }) => {
        pushEvent = (type, data) => client.send({ event: type, data })
      }),
    )

    const qc = makeQueryClient()
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')

    const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.connectionState).toBe('connected'))

    act(() => {
      pushEvent('approval.resolved', JSON.stringify({ approvalId: 'a1' }))
    })

    await waitFor(() => {
      const keys = invalidateSpy.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey)
      expect(keys).toContainEqual(['approvals'])
      expect(keys).toContainEqual(['runs'])
    })
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

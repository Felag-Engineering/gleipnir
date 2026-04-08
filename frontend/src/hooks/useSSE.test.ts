import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useSSE } from './useSSE'
import { queryKeys } from '@/hooks/queryKeys'

// ---------------------------------------------------------------------------
// Test harness helpers
// ---------------------------------------------------------------------------

/**
 * Flush the microtask queue several times so that Promise callbacks (including
 * the async connect() loop) can make progress after a timer advance.
 * Must be called inside act() when state updates are expected.
 */
async function flushPromises() {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

type FakeStream = {
  /** A response-like object to return from the mocked fetch. */
  responseLike: {
    ok: true
    status: 200
    body: { getReader: () => FakeReader }
  }
  /** Push a chunk of bytes into the stream. */
  push(bytes: Uint8Array): void
  /** Signal clean end-of-stream. */
  close(): void
  /** Reject the next pending read() with an error. */
  error(err: unknown): void
}

type FakeReader = {
  read(): Promise<{ value: Uint8Array; done: false } | { value: undefined; done: true }>
  cancel(): Promise<void>
}

/**
 * Returns a fake stream that duck-types the parts of Response + ReadableStreamDefaultReader
 * that useSSE touches. Using real ReadableStream/Response in jsdom is unreliable.
 *
 * Internally it maintains two queues:
 *  - pendingReaders: resolvers waiting for a chunk
 *  - bufferedChunks: chunks waiting for a reader
 * so push/close/error are safe to call before or after read().
 */
function makeFakeStream(): FakeStream {
  const pendingReaders: Array<{
    resolve: (v: { value: Uint8Array; done: false } | { value: undefined; done: true }) => void
    reject: (err: unknown) => void
  }> = []
  const bufferedChunks: Array<
    { type: 'value'; bytes: Uint8Array } | { type: 'done' } | { type: 'error'; err: unknown }
  > = []

  const readerLike: FakeReader = {
    read() {
      // If there is already a buffered chunk, resolve immediately.
      const next = bufferedChunks.shift()
      if (next !== undefined) {
        if (next.type === 'value') return Promise.resolve({ value: next.bytes, done: false as const })
        if (next.type === 'done') return Promise.resolve({ value: undefined, done: true as const })
        return Promise.reject(next.err)
      }
      // Otherwise park a resolver and wait for push/close/error.
      return new Promise((resolve, reject) => {
        pendingReaders.push({ resolve, reject })
      })
    },
    cancel() {
      return Promise.resolve()
    },
  }

  function deliver(
    chunk:
      | { type: 'value'; bytes: Uint8Array }
      | { type: 'done' }
      | { type: 'error'; err: unknown },
  ) {
    const waiting = pendingReaders.shift()
    if (waiting !== undefined) {
      if (chunk.type === 'value') waiting.resolve({ value: chunk.bytes, done: false as const })
      else if (chunk.type === 'done') waiting.resolve({ value: undefined, done: true as const })
      else waiting.reject(chunk.err)
    } else {
      bufferedChunks.push(chunk)
    }
  }

  return {
    responseLike: {
      ok: true,
      status: 200,
      body: { getReader: () => readerLike },
    },
    push(bytes) {
      deliver({ type: 'value', bytes })
    },
    close() {
      deliver({ type: 'done' })
    },
    error(err) {
      deliver({ type: 'error', err })
    },
  }
}

const encoder = new TextEncoder()

/**
 * Encode an SSE frame as Uint8Array. The id field is included only when provided.
 */
function frame(event: string, data: string, id?: string): Uint8Array {
  const idLine = id !== undefined ? `id: ${id}\n` : ''
  return encoder.encode(`${idLine}event: ${event}\ndata: ${data}\n\n`)
}

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function makeWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client: queryClient }, children)
  }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('useSSE', () => {
  // Fake timers scoped to setTimeout/clearTimeout/Date only — leaves microtasks
  // and Promise jobs real so the async connect() loop can make progress.
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  // (a) Table-driven: every SSE event type dispatches correct query invalidations.
  it.each([
    [
      'run.status_changed',
      JSON.stringify({ runId: 'r1' }),
      undefined,
      [queryKeys.runs.all, queryKeys.policies.all, queryKeys.stats.all, queryKeys.attention.all],
    ],
    [
      'run.step_added',
      JSON.stringify({ run_id: 'r1', step_id: 's1', step_number: 0, type: 'thought' }),
      undefined,
      [queryKeys.runs.steps('r1')],
    ],
    [
      'approval.created',
      JSON.stringify({ approvalId: 'a1' }),
      undefined,
      [queryKeys.approvals.all, queryKeys.stats.all, queryKeys.attention.all],
    ],
    [
      'approval.resolved',
      JSON.stringify({ approvalId: 'a1' }),
      undefined,
      [queryKeys.approvals.all, queryKeys.runs.all, queryKeys.stats.all, queryKeys.attention.all],
    ],
    [
      'feedback.created',
      JSON.stringify({ run_id: 'r1' }),
      undefined,
      [queryKeys.runs.all, queryKeys.stats.all, queryKeys.attention.all],
    ],
    [
      'feedback.resolved',
      JSON.stringify({ run_id: 'r1' }),
      undefined,
      [
        queryKeys.runs.all,
        queryKeys.stats.all,
        queryKeys.attention.all,
        queryKeys.runs.detail('r1'),
        queryKeys.runs.steps('r1'),
      ],
    ],
    [
      'feedback.timed_out',
      JSON.stringify({ run_id: 'r1' }),
      undefined,
      [
        queryKeys.runs.all,
        queryKeys.stats.all,
        queryKeys.attention.all,
        queryKeys.runs.detail('r1'),
        queryKeys.runs.steps('r1'),
      ],
    ],
  ] as [string, string, string | undefined, unknown[]][])(
    'invalidates correct query keys on %s',
    async (eventType, payload, id, expectedKeys) => {
      const stream = makeFakeStream()
      const mockFetch = vi.fn().mockResolvedValueOnce(stream.responseLike)
      vi.stubGlobal('fetch', mockFetch)

      const qc = makeQueryClient()
      const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')

      renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })

      // Let connect() progress and the 'connected' state update land.
      await act(async () => {
        await flushPromises()
      })

      // Push the event frame and let the read loop process it.
      await act(async () => {
        stream.push(frame(eventType, payload, id))
        await flushPromises()
      })

      const keys = invalidateSpy.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey)
      for (const expected of expectedKeys) {
        expect(keys).toContainEqual(expected)
      }
    },
  )

  // (b) Last-Event-ID is sent on reconnect (lowercase header) and absent on first connect.
  it('sends last-event-id header on reconnect but not on first connect', async () => {
    const firstStream = makeFakeStream()
    const secondStream = makeFakeStream()
    const mockFetch = vi.fn()
      .mockResolvedValueOnce(firstStream.responseLike)
      .mockResolvedValueOnce(secondStream.responseLike)
    vi.stubGlobal('fetch', mockFetch)

    const qc = makeQueryClient()
    renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })

    // Wait for the first connection to be established.
    await act(async () => {
      await flushPromises()
    })

    // Push an event with id=42, then close so the hook schedules a reconnect.
    await act(async () => {
      firstStream.push(frame('run.status_changed', JSON.stringify({ runId: 'r1' }), '42'))
      await flushPromises()
      firstStream.close()
      await flushPromises()
    })

    // Advance past the 1s backoff delay to trigger the reconnect.
    await act(async () => {
      vi.advanceTimersByTime(1000)
      await flushPromises()
    })

    expect(mockFetch).toHaveBeenCalledTimes(2)

    const firstInit = mockFetch.mock.calls[0][1] as RequestInit
    expect(new Headers(firstInit.headers).get('last-event-id')).toBeNull()

    const secondInit = mockFetch.mock.calls[1][1] as RequestInit
    expect(new Headers(secondInit.headers).get('last-event-id')).toBe('42')
  })

  // (c) Backoff schedule: 1s → 2s → 5s → 15s → 15s, state transitions are correct.
  it('escalates backoff on consecutive failures and resets after success', async () => {
    const successStream = makeFakeStream()
    const mockFetch = vi.fn()
      // Five rejecting responses to drive through the backoff schedule.
      .mockRejectedValueOnce(new Error('network error'))
      .mockRejectedValueOnce(new Error('network error'))
      .mockRejectedValueOnce(new Error('network error'))
      .mockRejectedValueOnce(new Error('network error'))
      .mockRejectedValueOnce(new Error('network error'))
      // Sixth call succeeds.
      .mockResolvedValueOnce(successStream.responseLike)
      // Seventh call fails again (to verify backoff reset to 1s).
      .mockRejectedValueOnce(new Error('network error'))
    vi.stubGlobal('fetch', mockFetch)

    const qc = makeQueryClient()
    const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })

    // Initial connect attempt (failure 1).
    await act(async () => {
      await flushPromises()
    })
    expect(result.current.connectionState).toBe('reconnecting')

    // failure 2 (after 1s backoff)
    await act(async () => {
      vi.advanceTimersByTime(1000)
      await flushPromises()
    })
    expect(result.current.connectionState).toBe('reconnecting')

    // failure 3 (after 2s backoff)
    await act(async () => {
      vi.advanceTimersByTime(2000)
      await flushPromises()
    })
    expect(result.current.connectionState).toBe('reconnecting')

    // failure 4 (after 5s backoff)
    await act(async () => {
      vi.advanceTimersByTime(5000)
      await flushPromises()
    })
    expect(result.current.connectionState).toBe('reconnecting')

    // failure 5 (after 15s backoff) — transitions to 'disconnected'
    await act(async () => {
      vi.advanceTimersByTime(15000)
      await flushPromises()
    })
    expect(result.current.connectionState).toBe('disconnected')

    // Sixth attempt (after 15s) succeeds — resets to 'connected'.
    await act(async () => {
      vi.advanceTimersByTime(15000)
      await flushPromises()
    })
    expect(result.current.connectionState).toBe('connected')

    // Close the success stream so the hook schedules another reconnect.
    // This verifies backoff reset: after a successful connect the first
    // failure should use the 1s delay, not the previous 15s.
    await act(async () => {
      successStream.close()
      await flushPromises()
    })

    // The failure from clean EOF schedules a reconnect with 1s delay.
    await act(async () => {
      vi.advanceTimersByTime(1000)
      await flushPromises()
    })
    expect(mockFetch).toHaveBeenCalledTimes(7)
    expect(result.current.connectionState).toBe('reconnecting')
  })

  // (d) Malformed JSON on a parseRunId event logs console.warn with structured data.
  it('logs console.warn with structured data on malformed JSON payload', async () => {
    const stream = makeFakeStream()
    const mockFetch = vi.fn().mockResolvedValueOnce(stream.responseLike)
    vi.stubGlobal('fetch', mockFetch)

    const qc = makeQueryClient()
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})

    const { result } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })

    await act(async () => {
      await flushPromises()
    })

    await act(async () => {
      stream.push(frame('run.step_added', 'not valid json', '7'))
      await flushPromises()
    })

    expect(warnSpy).toHaveBeenCalledWith(
      'useSSE: failed to parse event payload',
      expect.objectContaining({
        eventType: 'run.step_added',
        lastEventId: '7',
        data: 'not valid json',
        error: expect.any(Error),
      }),
    )

    // Hook should still be connected after a parse error.
    expect(result.current.connectionState).toBe('connected')
  })

  // (e) Unmounting aborts the in-flight fetch and no state updates occur afterward.
  it('aborts the fetch on unmount', async () => {
    const stream = makeFakeStream()
    const mockFetch = vi.fn().mockResolvedValueOnce(stream.responseLike)
    vi.stubGlobal('fetch', mockFetch)

    const abortSpy = vi.spyOn(AbortController.prototype, 'abort')

    const qc = makeQueryClient()
    const { unmount } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })

    await act(async () => {
      await flushPromises()
    })

    unmount()

    expect(abortSpy).toHaveBeenCalledTimes(1)

    // Advance well past any backoff delay to confirm no reconnect was scheduled
    // after unmount. React Testing Library would throw if setState was called
    // on an unmounted component.
    await act(async () => {
      vi.advanceTimersByTime(60000)
      await flushPromises()
    })
  })

  // (f) Idle watchdog: no bytes for 30s triggers a reconnect.
  it('reconnects after 30s of silence (idle watchdog)', async () => {
    const silentStream = makeFakeStream()
    const secondStream = makeFakeStream()
    const mockFetch = vi.fn()
      .mockResolvedValueOnce(silentStream.responseLike)
      .mockResolvedValueOnce(secondStream.responseLike)
    vi.stubGlobal('fetch', mockFetch)

    const qc = makeQueryClient()
    renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })

    await act(async () => {
      await flushPromises()
    })

    // No bytes are pushed. After 30s the watchdog aborts the connection.
    await act(async () => {
      vi.advanceTimersByTime(30000)
      await flushPromises()
    })

    // The abort triggers scheduleReconnect() with a 1s delay.
    await act(async () => {
      vi.advanceTimersByTime(1000)
      await flushPromises()
    })

    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  // (g) StrictMode double-mount: immediate unmount before any promise resolves
  // must not leak state updates onto the unmounted component.
  it('handles StrictMode-style immediate unmount safely', async () => {
    const stream = makeFakeStream()
    const mockFetch = vi.fn().mockResolvedValue(stream.responseLike)
    vi.stubGlobal('fetch', mockFetch)

    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    const qc = makeQueryClient()
    const { unmount } = renderHook(() => useSSE(), { wrapper: makeWrapper(qc) })

    // Unmount immediately, before any microtask has a chance to run.
    unmount()

    // Now let the async connect() progress and timers tick.
    await act(async () => {
      await flushPromises()
      vi.advanceTimersByTime(60000)
      await flushPromises()
    })

    // React will call console.error if setState is called on an unmounted component.
    expect(errorSpy).not.toHaveBeenCalled()
  })
})

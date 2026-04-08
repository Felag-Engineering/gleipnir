import { useEffect, useRef, useState } from 'react'
import { useQueryClient, QueryKey } from '@tanstack/react-query'
import { queryKeys } from './queryKeys'

export type ConnectionState = 'connected' | 'disconnected' | 'reconnecting'

type InvalidationSpec = {
  /** Query keys to always invalidate when this event fires */
  keys: readonly QueryKey[]
  /** If true, parse event data as { run_id: string } and also invalidate run-specific keys */
  parseRunId?: boolean
  /** Additional run-specific keys to invalidate when parseRunId is true */
  runKeys?: (runId: string) => QueryKey[]
}

const SSE_INVALIDATIONS: Record<string, InvalidationSpec> = {
  'run.status_changed': {
    keys: [queryKeys.runs.all, queryKeys.policies.all, queryKeys.stats.all, queryKeys.attention.all],
  },
  'run.step_added': {
    keys: [],
    parseRunId: true,
    runKeys: (id) => [queryKeys.runs.steps(id)],
  },
  'approval.created': {
    keys: [queryKeys.approvals.all, queryKeys.stats.all, queryKeys.attention.all],
  },
  'approval.resolved': {
    keys: [queryKeys.approvals.all, queryKeys.runs.all, queryKeys.stats.all, queryKeys.attention.all],
  },
  'feedback.created': {
    keys: [queryKeys.runs.all, queryKeys.stats.all, queryKeys.attention.all],
  },
  'feedback.resolved': {
    keys: [queryKeys.runs.all, queryKeys.stats.all, queryKeys.attention.all],
    parseRunId: true,
    runKeys: (id) => [queryKeys.runs.detail(id), queryKeys.runs.steps(id)],
  },
  'feedback.timed_out': {
    keys: [queryKeys.runs.all, queryKeys.stats.all, queryKeys.attention.all],
    parseRunId: true,
    runKeys: (id) => [queryKeys.runs.detail(id), queryKeys.runs.steps(id)],
  },
}

// Backoff delay in ms for consecutive failures: 1s, 2s, 5s, 15s (held at 15s).
const BACKOFF_DELAYS = [1000, 2000, 5000, 15000] as const

// After this many consecutive failures, state transitions to 'disconnected'.
// The first four failures stay 'reconnecting'; the fifth triggers 'disconnected'.
const DISCONNECTED_THRESHOLD = 5

// Idle watchdog timeout in ms. Backend sends keepalive heartbeats every 15s,
// so 30s gives two missed beats before we forcibly reconnect.
const IDLE_TIMEOUT_MS = 30_000

export function useSSE(): { connectionState: ConnectionState } {
  const queryClient = useQueryClient()
  // Initial state is 'reconnecting' so the ConnectionBanner shows immediately
  // before the first connect attempt. This matches the pre-existing behaviour —
  // ConnectionBanner hides only on 'connected', so this avoids a blank-then-banner flash.
  const [connectionState, setConnectionState] = useState<ConnectionState>('reconnecting')

  const lastEventIdRef = useRef<string | null>(null)
  const controllerRef = useRef<AbortController | null>(null)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const idleTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const backoffIndexRef = useRef<number>(0)
  const failureCountRef = useRef<number>(0)
  // cancelledRef is set true BEFORE aborting the controller so that any in-flight
  // await that resumes after the abort sees the flag and returns without touching
  // state. This closes the TOCTOU window in React StrictMode's double-invocation.
  const cancelledRef = useRef<boolean>(false)

  useEffect(() => {
    cancelledRef.current = false

    function clearIdleWatchdog() {
      if (idleTimerRef.current !== null) {
        clearTimeout(idleTimerRef.current)
        idleTimerRef.current = null
      }
    }

    function resetIdleWatchdog(controller: AbortController) {
      clearIdleWatchdog()
      idleTimerRef.current = setTimeout(() => {
        // No bytes arrived in 30s (including heartbeat comments). Abort so the
        // reader loop unwinds and scheduleReconnect() handles the retry.
        controller.abort()
      }, IDLE_TIMEOUT_MS)
    }

    function scheduleReconnect() {
      // If the cleanup function has already run, do not schedule anything.
      if (cancelledRef.current) return

      // Abort any current read loop and clear the idle watchdog.
      controllerRef.current?.abort()
      clearIdleWatchdog()

      failureCountRef.current += 1
      const delayIndex = Math.min(backoffIndexRef.current, 3)
      const delay = BACKOFF_DELAYS[delayIndex]
      backoffIndexRef.current = Math.min(backoffIndexRef.current + 1, 3)

      // Transition to 'disconnected' only after DISCONNECTED_THRESHOLD consecutive
      // failures so the UI does not flash 'disconnected' on a momentary hiccup.
      if (failureCountRef.current >= DISCONNECTED_THRESHOLD) {
        setConnectionState('disconnected')
      } else {
        setConnectionState('reconnecting')
      }

      reconnectTimerRef.current = setTimeout(() => {
        if (!cancelledRef.current) connect()
      }, delay)
    }

    function dispatchInvalidations(eventType: string, data: string, id: string | undefined) {
      const spec = SSE_INVALIDATIONS[eventType]
      if (!spec) return

      for (const key of spec.keys) {
        queryClient.invalidateQueries({ queryKey: key as QueryKey })
      }

      if (spec.parseRunId) {
        try {
          const parsed: { run_id: string } = JSON.parse(data)
          for (const key of spec.runKeys?.(parsed.run_id) ?? []) {
            queryClient.invalidateQueries({ queryKey: key as QueryKey })
          }
        } catch (error) {
          console.warn('useSSE: failed to parse event payload', {
            eventType,
            lastEventId: id,
            error,
            data,
          })
        }
      }
    }

    // Parses a complete SSE frame (the text between two \n\n separators) and
    // dispatches any recognised event type. Returns the event id if one was present.
    function parseAndDispatchFrame(frame: string): string | undefined {
      let frameId: string | undefined
      let eventType = 'message'
      let dataLines: string[] = []

      for (const line of frame.split('\n')) {
        if (line.startsWith(':')) {
          // Comment line (e.g. ": keepalive") — no action needed.
          continue
        } else if (line.startsWith('id:')) {
          frameId = line.slice(3).trim()
        } else if (line.startsWith('event:')) {
          eventType = line.slice(6).trim()
        } else if (line.startsWith('data:')) {
          dataLines.push(line.slice(5).trim())
        }
      }

      if (frameId !== undefined) {
        lastEventIdRef.current = frameId
      }

      const data = dataLines.join('\n')
      if (eventType in SSE_INVALIDATIONS) {
        dispatchInvalidations(eventType, data, frameId)
      }

      return frameId
    }

    async function connect() {
      const controller = new AbortController()
      controllerRef.current = controller

      // Clear any leftover idle watchdog from a prior connection attempt.
      clearIdleWatchdog()

      const headers: Record<string, string> = {
        Accept: 'text/event-stream',
        'Cache-Control': 'no-cache',
      }

      // Only send Last-Event-ID on reconnect, not on the initial connect.
      if (lastEventIdRef.current !== null) {
        headers['Last-Event-ID'] = lastEventIdRef.current
      }

      let response: Response
      try {
        response = await fetch('/api/v1/events', {
          signal: controller.signal,
          headers,
          // credentials: 'same-origin' matches the EventSource default and works
          // with the Vite dev proxy and the embedded prod build. Do NOT change to
          // 'include' — backend does not set Access-Control-Allow-Credentials, and
          // CORS will break.
          credentials: 'same-origin',
          cache: 'no-store',
        })
      } catch (err) {
        // If we were cancelled (cleanup ran), the AbortError is expected — return silently.
        if (cancelledRef.current || controller.signal.aborted) return
        scheduleReconnect()
        return
      }

      // Abort/cancel check after the await — StrictMode may have run cleanup
      // while fetch was in flight.
      if (cancelledRef.current || controller.signal.aborted) return

      if (!response.ok) {
        scheduleReconnect()
        return
      }

      // Successful connection: reset failure tracking and update UI state.
      setConnectionState('connected')
      backoffIndexRef.current = 0
      failureCountRef.current = 0

      const reader = response.body!.getReader()
      const decoder = new TextDecoder('utf-8', { fatal: false })
      let buffer = ''

      // Start the idle watchdog. It will be reset on every incoming chunk.
      resetIdleWatchdog(controller)

      // A promise that rejects when the AbortController fires. This lets us
      // race reader.read() against abort so the loop unwinds even when the
      // underlying stream (e.g. in tests with a fake reader) doesn't auto-reject
      // on signal abort the way a real fetch body would.
      const abortPromise = new Promise<never>((_, reject) => {
        controller.signal.addEventListener('abort', () => {
          reject(new DOMException('Aborted', 'AbortError'))
        }, { once: true })
      })

      try {
        while (true) {
          const { value, done } = await Promise.race([reader.read(), abortPromise])

          // Check after every await — cleanup may have aborted during the read.
          if (cancelledRef.current || controller.signal.aborted) {
            reader.cancel()
            return
          }

          if (done) {
            // Clean EOF from the server is treated as an unexpected disconnect.
            // The backend never closes the stream intentionally mid-run.
            clearIdleWatchdog()
            scheduleReconnect()
            return
          }

          // Bytes arrived — reset the idle watchdog so it doesn't fire as long
          // as the backend keeps sending heartbeats (every 15s).
          resetIdleWatchdog(controller)

          buffer += decoder.decode(value, { stream: true })

          // SSE frames are separated by blank lines (\n\n or \r\n\r\n).
          // Split on both forms; trailing partial frames stay in the buffer.
          const frames = buffer.split(/\r?\n\r?\n/)
          // The last element is either empty (clean split) or an incomplete frame.
          buffer = frames.pop() ?? ''

          for (const frame of frames) {
            if (frame.trim() === '') continue
            parseAndDispatchFrame(frame)
          }
        }
      } catch (err) {
        // An AbortError here means either our cleanup ran or the idle watchdog fired.
        if (cancelledRef.current || controller.signal.aborted) {
          // If cleanup ran, return silently. If the watchdog fired (our own abort),
          // cancelledRef is still false, so we fall through to scheduleReconnect.
          if (cancelledRef.current) return
        }
        clearIdleWatchdog()
        scheduleReconnect()
      }
    }

    connect()

    return () => {
      // Set cancelledRef BEFORE aborting — this ensures any in-flight await that
      // resumes after the abort observes cancelled=true and does not touch state.
      cancelledRef.current = true
      if (reconnectTimerRef.current !== null) {
        clearTimeout(reconnectTimerRef.current)
        reconnectTimerRef.current = null
      }
      if (idleTimerRef.current !== null) {
        clearTimeout(idleTimerRef.current)
        idleTimerRef.current = null
      }
      controllerRef.current?.abort()
    }
  }, [queryClient])

  return { connectionState }
}

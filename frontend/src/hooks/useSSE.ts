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

export function useSSE(): { connectionState: ConnectionState } {
  const queryClient = useQueryClient()
  const [connectionState, setConnectionState] = useState<ConnectionState>('reconnecting')
  // Tracked for diagnostics and future reconnection support (e.g. Last-Event-ID header).
  const lastEventIdRef = useRef<string | null>(null)

  useEffect(() => {
    const eventSource = new EventSource('/api/v1/events')

    eventSource.onopen = () => {
      setConnectionState('connected')
    }

    eventSource.onerror = () => {
      if (eventSource.readyState === EventSource.CLOSED) {
        setConnectionState('disconnected')
      } else {
        setConnectionState('reconnecting')
      }
    }

    for (const [eventType, spec] of Object.entries(SSE_INVALIDATIONS)) {
      eventSource.addEventListener(eventType, (e: MessageEvent) => {
        if (e.lastEventId) lastEventIdRef.current = e.lastEventId

        for (const key of spec.keys) {
          queryClient.invalidateQueries({ queryKey: key as QueryKey })
        }

        if (spec.parseRunId) {
          try {
            const data: { run_id: string } = JSON.parse(e.data)
            for (const key of spec.runKeys?.(data.run_id) ?? []) {
              queryClient.invalidateQueries({ queryKey: key as QueryKey })
            }
          } catch {
            console.error(`useSSE: failed to parse ${eventType} payload`, e.data)
          }
        }
      })
    }

    return () => {
      eventSource.close()
    }
  }, [queryClient])

  return { connectionState }
}

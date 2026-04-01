import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { queryKeys } from './queryKeys'

export type ConnectionState = 'connected' | 'disconnected' | 'reconnecting'

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

    eventSource.addEventListener('run.status_changed', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
    })

    eventSource.addEventListener('run.step_added', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      try {
        const data: { run_id: string } = JSON.parse(e.data)
        queryClient.invalidateQueries({ queryKey: queryKeys.runs.steps(data.run_id) })
      } catch {
        console.error('useSSE: failed to parse run.step_added payload', e.data)
      }
    })

    eventSource.addEventListener('approval.created', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      queryClient.invalidateQueries({ queryKey: queryKeys.approvals.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
    })

    eventSource.addEventListener('approval.resolved', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      queryClient.invalidateQueries({ queryKey: queryKeys.approvals.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
    })

    eventSource.addEventListener('feedback.created', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
    })

    eventSource.addEventListener('feedback.resolved', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      try {
        const data: { run_id: string } = JSON.parse(e.data)
        queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })
        queryClient.invalidateQueries({ queryKey: queryKeys.runs.detail(data.run_id) })
        queryClient.invalidateQueries({ queryKey: queryKeys.runs.steps(data.run_id) })
        queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
      } catch {
        console.error('useSSE: failed to parse feedback.resolved payload', e.data)
      }
    })

    eventSource.addEventListener('feedback.timed_out', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      try {
        const data: { run_id: string } = JSON.parse(e.data)
        queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })
        queryClient.invalidateQueries({ queryKey: queryKeys.runs.detail(data.run_id) })
        queryClient.invalidateQueries({ queryKey: queryKeys.runs.steps(data.run_id) })
        queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
      } catch {
        console.error('useSSE: failed to parse feedback.timed_out payload', e.data)
      }
    })

    return () => {
      eventSource.close()
    }
  }, [queryClient])

  return { connectionState }
}

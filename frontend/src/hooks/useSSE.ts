import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

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
      queryClient.invalidateQueries({ queryKey: ['runs'] })
      queryClient.invalidateQueries({ queryKey: ['policies'] })
    })

    eventSource.addEventListener('run.step_added', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      try {
        const data: { runId: string; step: unknown } = JSON.parse(e.data)
        queryClient.setQueryData(
          ['runs', data.runId, 'steps'],
          (old: unknown[] | undefined) => (old ? [...old, data.step] : [data.step]),
        )
      } catch {
        console.error('useSSE: failed to parse run.step_added payload', e.data)
      }
    })

    eventSource.addEventListener('approval.created', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      queryClient.invalidateQueries({ queryKey: ['approvals'] })
    })

    eventSource.addEventListener('approval.resolved', (e: MessageEvent) => {
      if (e.lastEventId) lastEventIdRef.current = e.lastEventId
      queryClient.invalidateQueries({ queryKey: ['approvals'] })
      queryClient.invalidateQueries({ queryKey: ['runs'] })
    })

    return () => {
      eventSource.close()
    }
  }, [queryClient])

  return { connectionState }
}

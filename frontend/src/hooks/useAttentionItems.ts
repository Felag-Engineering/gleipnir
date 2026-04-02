import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiAttentionResponse, ApiAttentionItem } from '@/api/types'
import { queryKeys } from './queryKeys'

export type AttentionItemType = 'approval' | 'feedback' | 'failure'

// AttentionItem is the frontend representation of an attention queue entry.
// It adds a computed sortKey for urgency ordering.
export interface AttentionItem extends ApiAttentionItem {
  sortKey: number
}

const DISMISSED_STORAGE_KEY = 'gleipnir-dismissed-failures'

function loadDismissedSet(): Set<string> {
  try {
    const raw = localStorage.getItem(DISMISSED_STORAGE_KEY)
    return raw ? new Set<string>(JSON.parse(raw) as string[]) : new Set()
  } catch {
    return new Set()
  }
}

function saveDismissedSet(ids: Set<string>): void {
  try {
    localStorage.setItem(DISMISSED_STORAGE_KEY, JSON.stringify([...ids]))
  } catch {
    // localStorage unavailable in some environments; silently ignore
  }
}

// sortKeyForItem computes the urgency sort key (Unix ms, ascending = most urgent first).
// Approval/feedback: expires_at timestamp. Failures: created_at + 24h.
function sortKeyForItem(item: ApiAttentionItem): number {
  if (item.expires_at) {
    return new Date(item.expires_at).getTime()
  }
  // Failures and feedback without expires_at auto-dismiss after 24h from created_at.
  return new Date(item.created_at).getTime() + 24 * 60 * 60 * 1000
}

// useAttentionItems fetches the attention queue from GET /api/v1/attention,
// filters out dismissed failures, and sorts all items by deadline urgency.
//
// Cache invalidation relies on SSE events (approval.created, approval.resolved,
// run.status_changed) which invalidate the attention query key. staleTime of
// 30s guards against excessive refetches when multiple SSE events arrive in
// rapid succession.
export function useAttentionItems() {
  // Toggle state is only used to force a re-render after dismissing a failure.
  const [, setDismissToggle] = useState(0)

  const query = useQuery({
    queryKey: queryKeys.attention.all,
    queryFn: () => apiFetch<ApiAttentionResponse>('/attention'),
    staleTime: 30_000,
  })

  const dismissed = loadDismissedSet()

  const items: AttentionItem[] = (query.data?.items ?? [])
    .filter(item => {
      if (item.type === 'failure') {
        return !dismissed.has(item.run_id)
      }
      return true
    })
    .map(item => ({ ...item, sortKey: sortKeyForItem(item) }))
    .sort((a, b) => a.sortKey - b.sortKey)

  function dismissFailure(runId: string) {
    const next = loadDismissedSet()
    next.add(runId)
    saveDismissedSet(next)
    setDismissToggle(n => n + 1)
  }

  return {
    items,
    count: items.length,
    isLoading: query.isLoading,
    dismissFailure,
  }
}

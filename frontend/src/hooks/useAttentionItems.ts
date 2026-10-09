import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiAttentionResponse, ApiAttentionItem } from '@/api/types'
import { permissionAskRunIds } from '@/utils/permissionAsks'
import { queryKeys } from './queryKeys'

export type AttentionItemType = 'approval' | 'feedback' | 'tool_input' | 'failure'

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

const ATTENTION_STALE_TIME = 30_000

function fetchAttention(): Promise<ApiAttentionResponse> {
  return apiFetch<ApiAttentionResponse>('/attention')
}

// useAttentionItems fetches the attention queue from GET /api/v1/attention,
// filters out dismissed failures, and sorts all items by deadline urgency.
//
// Cache invalidation relies on SSE events (approval.created, approval.resolved,
// run.status_changed) which invalidate the attention query key. staleTime of
// 30s guards against excessive refetches when multiple SSE events arrive in
// rapid succession.
// `enabled: false` skips the request — used before the session is known.
export function useAttentionItems({ enabled = true }: { enabled?: boolean } = {}) {
  // Toggle state is only used to force a re-render after dismissing a failure.
  const [, setDismissToggle] = useState(0)

  const query = useQuery({
    queryKey: queryKeys.attention.all,
    queryFn: fetchAttention,
    staleTime: ATTENTION_STALE_TIME,
    enabled,
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

// usePermissionAskRunIds returns the IDs of runs currently paused on a
// tool-initiated *permission* ask (a `tool_input` attention row whose
// elicitation_kind is `permission`). Run lists use it to label such a run
// "Awaiting Approval" rather than "Awaiting Feedback".
//
// It reads the same cached attention query the sidebar already keeps warm for
// every page (same key, same fetcher), so a list of N runs costs no extra
// request — never one per row. SSE `tool_input.*` and `run.status_changed`
// events invalidate that key, so the set follows the queue.
export function usePermissionAskRunIds(): ReadonlySet<string> {
  const query = useQuery({
    queryKey: queryKeys.attention.all,
    queryFn: fetchAttention,
    staleTime: ATTENTION_STALE_TIME,
  })
  return permissionAskRunIds(query.data?.items ?? [])
}

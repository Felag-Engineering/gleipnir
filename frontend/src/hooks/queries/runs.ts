import { useQuery, keepPreviousData } from '@tanstack/react-query'
import { useState, useEffect, useMemo } from 'react'
import { apiFetch, ApiError } from '@/api/fetch'
import type { ApiRun, ApiRunDecision, ApiRunResponder, ApiRunsResponse, ApiRunStep, ApiToolInputRequest } from '@/api/types'
import { queryKeys } from '../queryKeys'

// PAGE_SIZE_STEPS is the number of steps fetched per request, used by both the
// hook and the backend handler (runs_handler.go defaultStepsLimit).
export const PAGE_SIZE_STEPS = 500

export function useRun(id?: string) {
  return useQuery({
    queryKey: queryKeys.runs.detail(id ?? ''),
    queryFn: () => apiFetch<ApiRun>(`/runs/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
  })
}

// useRunDecisions loads a run's tool-initiated HITL decision records
// (ADR-055 §6.6).
//
// Separate from useRunSteps on purpose, and not merely for convenience: the
// two endpoints answer different questions. `/steps` is the trace the model was
// replayed; `/decisions` is oversight evidence the model never saw (ADR-046).
// A combined timeline interleaves them by timestamp, which is a presentation
// decision and stays on the presentation side.
//
// Unpaginated: a run's decisions are bounded by its elicitation budget, so
// there is no page to turn.
export function useRunDecisions(id?: string) {
  const query = useQuery({
    queryKey: queryKeys.runs.decisions(id ?? ''),
    queryFn: () => apiFetch<ApiRunDecision[]>(`/runs/${encodeURIComponent(id!)}/decisions`),
    enabled: Boolean(id),
  })

  return { ...query, decisions: query.data ?? [] }
}

// useRunResponders loads who settled each of a run's approval and feedback
// requests, keyed by request id. Separate from useRunSteps for the same reason
// as useRunDecisions: the answerer's identity is operator-facing evidence, not
// part of the trace the model was replayed (ADR-046).
export function useRunResponders(id?: string) {
  const query = useQuery({
    queryKey: queryKeys.runs.responders(id ?? ''),
    queryFn: () => apiFetch<ApiRunResponder[]>(`/runs/${encodeURIComponent(id!)}/responders`),
    enabled: Boolean(id),
  })

  const responders = useMemo(
    () => new Map((query.data ?? []).map((r) => [r.request_id, r])),
    [query.data],
  )
  return { ...query, responders }
}

// useToolInput loads the tool-initiated request a run is paused on
// (ADR-055 §6.1), or nothing when it is not paused on one.
//
// A 404 is the ordinary answer for a run with no pending request, so it is not
// retried and not surfaced as an error — a run that simply is not waiting on a
// human should not paint an error state onto its detail page. Anything else is
// a real failure and stays an error.
export function useToolInput(id?: string, enabled = true) {
  const query = useQuery({
    queryKey: queryKeys.runs.toolInput(id ?? ''),
    queryFn: async () => {
      try {
        return await apiFetch<ApiToolInputRequest>(`/runs/${encodeURIComponent(id!)}/tool-input`)
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) return null
        throw err
      }
    },
    enabled: Boolean(id) && enabled,
    retry: false,
  })

  return { ...query, request: query.data ?? null }
}

export interface RunsFilterParams {
  status?: string
  policy_id?: string
  since?: string
  until?: string
  sort?: string
  order?: string
  limit?: number
  offset?: number
}

export function useRuns(params: RunsFilterParams) {
  const paramRecord: Record<string, string> = {}
  if (params.status) paramRecord.status = params.status
  if (params.policy_id) paramRecord.policy_id = params.policy_id
  if (params.since) paramRecord.since = params.since
  if (params.until) paramRecord.until = params.until
  if (params.sort) paramRecord.sort = params.sort
  if (params.order) paramRecord.order = params.order
  if (params.limit != null) paramRecord.limit = String(params.limit)
  if (params.offset != null) paramRecord.offset = String(params.offset)

  const qs = new URLSearchParams(paramRecord).toString()

  const query = useQuery({
    queryKey: queryKeys.runs.list(paramRecord),
    queryFn: () => apiFetch<ApiRunsResponse>(`/runs${qs ? '?' + qs : ''}`),
    placeholderData: keepPreviousData,
  })

  return {
    ...query,
    runs: query.data?.runs ?? [],
    total: query.data?.total ?? 0,
  }
}

export interface UseRunStepsResult {
  steps: ApiRunStep[]
  status: 'pending' | 'error' | 'success'
  hasMore: boolean
  loadMore: () => Promise<void>
}

// useRunSteps loads the first page of steps with useQuery so TanStack Query
// handles caching, deduplication, and SSE-driven invalidations. Additional
// pages requested via loadMore are stored in local state (extraPages).
//
// Cache-invalidation contract: any refetch of the baseline first-page query
// resets extraPages to []. This means a SSE run.step_added invalidation or a
// mutation that calls queryClient.invalidateQueries will drop previously-loaded
// older pages. The user can reload them by clicking "Load more steps" again.
// This is simpler than a merge strategy and avoids silent data inconsistency.
export function useRunSteps(id?: string): UseRunStepsResult {
  const [extraPages, setExtraPages] = useState<ApiRunStep[][]>([])

  const baseQuery = useQuery({
    queryKey: queryKeys.runs.steps(id ?? ''),
    queryFn: () =>
      apiFetch<ApiRunStep[]>(
        `/runs/${encodeURIComponent(id!)}/steps?limit=${PAGE_SIZE_STEPS}`,
      ),
    enabled: Boolean(id),
  })

  // Reset extraPages whenever the baseline query result is refreshed. We key
  // on dataUpdatedAt so that a background refetch (e.g. from SSE invalidation
  // or an approval/feedback mutation) clears any previously-loaded older steps.
  // This avoids stale-data inconsistencies at the cost of a brief flicker.
  // extraPages is local state; any refetch of the first page wipes it.
  useEffect(() => {
    setExtraPages([])
  }, [baseQuery.dataUpdatedAt])

  const firstPage = baseQuery.data
  // Memoized so the array keeps its identity until a page actually changes:
  // useRunTimeline memoizes its parsing on this reference.
  const allSteps: ApiRunStep[] = useMemo(
    () => [firstPage ?? [], ...extraPages].flat(),
    [firstPage, extraPages],
  )

  const lastStep = allSteps[allSteps.length - 1]
  const lastStepNumber = lastStep?.step_number ?? -1

  // The server returns up to PAGE_SIZE_STEPS. Receiving exactly PAGE_SIZE_STEPS
  // rows means there may be more — the client infers hasMore from the count.
  const lastPageLen =
    extraPages.length > 0
      ? extraPages[extraPages.length - 1].length
      : (firstPage?.length ?? 0)
  const hasMore = lastPageLen === PAGE_SIZE_STEPS

  async function loadMore() {
    if (!id) return
    const page = await apiFetch<ApiRunStep[]>(
      `/runs/${encodeURIComponent(id)}/steps?after=${lastStepNumber}&limit=${PAGE_SIZE_STEPS}`,
    )
    setExtraPages((prev) => [...prev, page])
  }

  return {
    steps: allSteps,
    status: baseQuery.status,
    hasMore,
    loadMore,
  }
}

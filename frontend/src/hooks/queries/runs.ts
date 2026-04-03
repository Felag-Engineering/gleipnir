import { useQuery, keepPreviousData } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiRun, ApiRunsResponse, ApiRunStep } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function useRun(id?: string) {
  return useQuery({
    queryKey: queryKeys.runs.detail(id ?? ''),
    queryFn: () => apiFetch<ApiRun>(`/runs/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
  })
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

export function useRunSteps(id?: string) {
  return useQuery({
    queryKey: queryKeys.runs.steps(id ?? ''),
    queryFn: () => apiFetch<ApiRunStep[]>(`/runs/${encodeURIComponent(id!)}/steps`),
    enabled: Boolean(id),
  })
}

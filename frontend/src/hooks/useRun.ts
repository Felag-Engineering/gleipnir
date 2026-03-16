import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiRun } from '../api/types'
import { queryKeys } from './queryKeys'

export function useRun(id?: string) {
  return useQuery({
    queryKey: queryKeys.runs.detail(id ?? ''),
    queryFn: () => apiFetch<ApiRun>(`/runs/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
  })
}

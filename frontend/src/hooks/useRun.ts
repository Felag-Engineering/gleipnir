import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiRun } from '../api/types'

export function useRun(id?: string) {
  return useQuery({
    queryKey: ['runs', id] as const,
    queryFn: () => apiFetch<ApiRun>(`/runs/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
  })
}

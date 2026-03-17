import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiRun } from '../api/types'
import { queryKeys } from './queryKeys'

interface UseRunsOptions {
  limit?: number
  status?: string
}

export function useRuns({ limit = 20, status }: UseRunsOptions = {}) {
  return useQuery({
    queryKey: queryKeys.runs.all,
    queryFn: () => {
      const params = new URLSearchParams()
      if (limit) params.set('limit', String(limit))
      if (status) params.set('status', status)
      return apiFetch<ApiRun[]>(`/runs?${params}`)
    },
  })
}

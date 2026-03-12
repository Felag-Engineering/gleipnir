import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiStats } from '../api/types'

export const STATS_QUERY_KEY = ['stats'] as const

export function useStats() {
  return useQuery({
    queryKey: STATS_QUERY_KEY,
    queryFn: () => apiFetch<ApiStats>('/stats'),
  })
}

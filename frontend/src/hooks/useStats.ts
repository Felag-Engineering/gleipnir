import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiStats } from '../api/types'
import { queryKeys } from './queryKeys'

export function useStats() {
  return useQuery({
    queryKey: queryKeys.stats.all,
    queryFn: () => apiFetch<ApiStats>('/stats'),
  })
}

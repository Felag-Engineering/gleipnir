import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiStats, ApiTimeSeriesResponse } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function useStats() {
  return useQuery({
    queryKey: queryKeys.stats.all,
    queryFn: () => apiFetch<ApiStats>('/stats'),
  })
}

export interface StatsData {
  activeRuns: number
  pendingApprovals: number
  isLoading: boolean
  isError: boolean
}

export function useStatsData(): StatsData {
  const statsQuery = useStats()
  const data = statsQuery.data

  return {
    activeRuns: data?.active_runs ?? 0,
    pendingApprovals: data?.pending_approvals ?? 0,
    isLoading: statsQuery.isLoading,
    isError: statsQuery.isError,
  }
}

// useTimeSeriesStats fetches hourly-bucketed run activity and token cost data
// from GET /api/v1/stats/timeseries. Refreshes every 60 seconds — charts don't
// need real-time precision, so we avoid SSE overhead here.
export function useTimeSeriesStats(window = '24h') {
  const query = useQuery({
    queryKey: queryKeys.stats.timeseries(window),
    queryFn: () => apiFetch<ApiTimeSeriesResponse>(`/stats/timeseries?window=${window}`),
    refetchInterval: 60_000,
  })

  return {
    data: query.data,
    isLoading: query.isLoading,
    isError: query.isError,
  }
}

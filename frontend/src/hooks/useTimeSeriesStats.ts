import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiTimeSeriesResponse } from '@/api/types'
import { queryKeys } from './queryKeys'

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

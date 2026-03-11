import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiRun } from '../api/types'

export const RUNS_QUERY_KEY_BASE = ['runs'] as const

export const latestRunQueryKey = (policyId: string) =>
  ['runs', { policyId, limit: 1 }] as const

export function useLatestRun(policyId: string) {
  return useQuery({
    queryKey: latestRunQueryKey(policyId),
    queryFn: () =>
      apiFetch<ApiRun[]>(`/runs?policy_id=${encodeURIComponent(policyId)}&limit=1`).then(
        (runs) => runs[0] ?? null,
      ),
    enabled: Boolean(policyId),
  })
}

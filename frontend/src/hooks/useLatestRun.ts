import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiRun } from '../api/types'
import { queryKeys } from './queryKeys'

export function useLatestRun(policyId: string) {
  return useQuery({
    queryKey: queryKeys.runs.latestByPolicy(policyId),
    queryFn: () =>
      apiFetch<ApiRun[]>(`/runs?policy_id=${encodeURIComponent(policyId)}&limit=1`).then(
        (runs) => runs[0] ?? null,
      ),
    enabled: Boolean(policyId),
  })
}

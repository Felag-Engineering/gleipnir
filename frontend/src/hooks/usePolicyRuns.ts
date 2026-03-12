import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiRun } from '@/api/types'

export const policyRunsQueryKey = (policyId: string | undefined) =>
  ['runs', { policyId }] as const

export function usePolicyRuns(policyId: string | undefined) {
  return useQuery({
    queryKey: policyRunsQueryKey(policyId),
    queryFn: () => apiFetch<ApiRun[]>(`/runs?policy_id=${encodeURIComponent(policyId!)}`),
    enabled: Boolean(policyId),
  })
}

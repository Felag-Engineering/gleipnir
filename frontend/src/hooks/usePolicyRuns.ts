import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiRun } from '@/api/types'
import { queryKeys } from './queryKeys'

export function usePolicyRuns(policyId: string | undefined) {
  return useQuery({
    queryKey: queryKeys.runs.byPolicy(policyId ?? ''),
    queryFn: () => apiFetch<ApiRun[]>(`/runs?policy_id=${encodeURIComponent(policyId!)}`),
    enabled: Boolean(policyId),
  })
}

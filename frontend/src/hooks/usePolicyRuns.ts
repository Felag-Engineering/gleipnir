import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiRunsResponse } from '@/api/types'
import { queryKeys } from './queryKeys'

export function usePolicyRuns(policyId: string | undefined) {
  const result = useQuery({
    queryKey: queryKeys.runs.byPolicy(policyId ?? ''),
    queryFn: () => apiFetch<ApiRunsResponse>(`/runs?policy_id=${encodeURIComponent(policyId!)}`),
    enabled: Boolean(policyId),
  })

  return {
    ...result,
    // Unwrap runs array for backward compatibility with PolicyRunsPage
    data: result.data?.runs,
  }
}

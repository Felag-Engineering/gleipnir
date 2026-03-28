import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiPolicyDetail } from '@/api/types'
import { queryKeys } from './queryKeys'

export function usePolicy(id?: string) {
  return useQuery({
    queryKey: id ? queryKeys.policies.detail(id) : queryKeys.policies.detail(''),
    queryFn: () => apiFetch<ApiPolicyDetail>(`/policies/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
  })
}

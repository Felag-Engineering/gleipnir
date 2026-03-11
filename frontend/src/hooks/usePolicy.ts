import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiPolicyDetail } from '../api/types'

export const policyQueryKey = (id: string) => ['policies', id] as const

export function usePolicy(id?: string) {
  return useQuery({
    queryKey: id ? policyQueryKey(id) : ['policies', ''],
    queryFn: () => apiFetch<ApiPolicyDetail>(`/policies/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
  })
}

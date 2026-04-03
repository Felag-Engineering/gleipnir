import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiPolicyListItem, ApiPolicyDetail } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function usePolicies() {
  return useQuery({
    queryKey: queryKeys.policies.all,
    queryFn: () => apiFetch<ApiPolicyListItem[]>('/policies'),
  })
}

export function usePolicy(id?: string) {
  return useQuery({
    queryKey: id ? queryKeys.policies.detail(id) : queryKeys.policies.detail(''),
    queryFn: () => apiFetch<ApiPolicyDetail>(`/policies/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
  })
}

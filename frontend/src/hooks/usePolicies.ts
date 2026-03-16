import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiPolicyListItem } from '../api/types'
import { queryKeys } from './queryKeys'

export function usePolicies() {
  return useQuery({
    queryKey: queryKeys.policies.all,
    queryFn: () => apiFetch<ApiPolicyListItem[]>('/policies'),
  })
}

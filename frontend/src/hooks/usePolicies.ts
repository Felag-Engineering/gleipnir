import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiPolicyListItem } from '../api/types'

export const POLICIES_QUERY_KEY = ['policies'] as const

export function usePolicies() {
  return useQuery({
    queryKey: POLICIES_QUERY_KEY,
    queryFn: () => apiFetch<ApiPolicyListItem[]>('/policies'),
  })
}

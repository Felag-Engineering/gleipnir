import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiUserIdentity } from '@/api/types'
import { queryKeys } from '../queryKeys'

// GET /api/v1/users/{id}/identities — admin-only. One row per plugin instance
// the user has an actor id on.
export function useUserIdentities(userId: string) {
  return useQuery({
    queryKey: queryKeys.users.identities(userId),
    queryFn: () => apiFetch<ApiUserIdentity[]>(`/users/${encodeURIComponent(userId)}/identities`),
  })
}

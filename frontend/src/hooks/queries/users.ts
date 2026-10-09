import { useQuery } from '@tanstack/react-query'
import { apiFetch, apiFetchAuthProbe } from '@/api/fetch'
import type { ApiUser } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function useUsers() {
  return useQuery({
    queryKey: queryKeys.users.all,
    queryFn: () => apiFetch<ApiUser[]>('/users'),
  })
}

interface CurrentUser {
  id: string
  username: string
  roles: string[]
}

// data is undefined while the probe is in flight, null when nobody is logged
// in, and the user otherwise.
export function useCurrentUser() {
  return useQuery({
    queryKey: queryKeys.currentUser.all,
    queryFn: () => apiFetchAuthProbe<CurrentUser>('/auth/me'),
    // Stale for 5 minutes — the current user changes rarely. A logged-out
    // answer is never reused, so signing in and landing on the app re-asks.
    staleTime: (query) => (query.state.data === null ? 0 : 5 * 60 * 1000),
  })
}

interface ModelInfo {
  name: string
  display_name: string
}

export interface ProviderModels {
  provider: string
  models: ModelInfo[]
}

// `enabled: false` skips the request — used where the current role's read
// would 403 (see permissions/roleAccess.ts).
export function useModels({ enabled = true }: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: queryKeys.models.all,
    queryFn: () => apiFetch<ProviderModels[]>('/models'),
    staleTime: 5 * 60 * 1000, // models don't change often, cache 5 min
    enabled,
  })
}

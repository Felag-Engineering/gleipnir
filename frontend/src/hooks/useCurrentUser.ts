import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import { queryKeys } from './queryKeys'

interface CurrentUser {
  id: string
  username: string
  roles: string[]
}

export function useCurrentUser() {
  return useQuery({
    queryKey: queryKeys.currentUser.all,
    queryFn: () => apiFetch<CurrentUser>('/auth/me'),
    // Stale for 5 minutes — the current user changes rarely.
    staleTime: 5 * 60 * 1000,
  })
}

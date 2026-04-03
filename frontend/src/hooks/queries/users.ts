import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
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

export function useCurrentUser() {
  return useQuery({
    queryKey: queryKeys.currentUser.all,
    queryFn: () => apiFetch<CurrentUser>('/auth/me'),
    // Stale for 5 minutes — the current user changes rarely.
    staleTime: 5 * 60 * 1000,
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

export function useModels() {
  return useQuery({
    queryKey: queryKeys.models.all,
    queryFn: () => apiFetch<ProviderModels[]>('/models'),
    staleTime: 5 * 60 * 1000, // models don't change often, cache 5 min
  })
}

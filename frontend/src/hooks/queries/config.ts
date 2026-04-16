import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiPublicConfig } from '@/api/types'
import { queryKeys } from '../queryKeys'

// usePublicConfig fetches non-sensitive runtime config from GET /api/v1/config.
// All authenticated roles (including operators and auditors) can call this endpoint.
// Use this hook instead of useAdminSettings() when you only need public_url —
// useAdminSettings() is admin-only and exposes all settings including sensitive ones.
export function usePublicConfig() {
  return useQuery({
    queryKey: queryKeys.config.all,
    queryFn: () => apiFetch<ApiPublicConfig>('/config'),
    staleTime: 5 * 60 * 1000, // 5 minutes — public_url changes rarely
  })
}

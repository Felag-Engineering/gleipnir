import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiUser } from '@/api/types'
import { queryKeys } from './queryKeys'

export function useUsers() {
  return useQuery({
    queryKey: queryKeys.users.all,
    queryFn: () => apiFetch<ApiUser[]>('/users'),
  })
}

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiUser } from '@/api/types'
import { queryKeys } from '../queryKeys'

interface CreateUserParams {
  username: string
  password: string
  roles: string[]
}

export function useCreateUser() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (params: CreateUserParams) =>
      apiFetch<ApiUser>('/users', {
        method: 'POST',
        body: JSON.stringify(params),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.users.all })
    },
  })
}

interface UpdateUserParams {
  id: string
  deactivated?: boolean
  roles?: string[]
}

export function useUpdateUser() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ id, ...body }: UpdateUserParams) =>
      apiFetch<ApiUser>(`/users/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.users.all })
    },
  })
}

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiUser, CreateUserRequest, UpdateUserRequest } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function useCreateUser() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (params: CreateUserRequest) =>
      apiFetch<ApiUser>('/users', {
        method: 'POST',
        body: JSON.stringify(params),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.users.all })
    },
  })
}

export function useUpdateUser() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ id, ...body }: UpdateUserRequest) =>
      apiFetch<ApiUser>(`/users/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.users.all })
    },
  })
}

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiUser } from '@/api/types'
import { queryKeys } from './queryKeys'

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

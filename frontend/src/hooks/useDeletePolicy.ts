import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetchVoid } from '../api/fetch'
import { queryKeys } from './queryKeys'

export function useDeletePolicy() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: string) =>
      apiFetchVoid(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })
    },
  })
}

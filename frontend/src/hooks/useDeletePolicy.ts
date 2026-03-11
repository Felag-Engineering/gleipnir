import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetchVoid } from '../api/fetch'
import { POLICIES_QUERY_KEY } from './usePolicies'

export function useDeletePolicy() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: string) =>
      apiFetchVoid(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: POLICIES_QUERY_KEY })
    },
  })
}

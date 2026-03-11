import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiPolicySaveResponse } from '../api/types'
import { POLICIES_QUERY_KEY } from './usePolicies'
import { policyQueryKey } from './usePolicy'

interface SavePolicyArgs {
  id?: string   // absent → POST (create), present → PUT (update)
  yaml: string  // raw YAML string sent as text/plain body
}

export function useSavePolicy() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ id, yaml }: SavePolicyArgs) => {
      const path = id ? `/policies/${encodeURIComponent(id)}` : '/policies'
      const method = id ? 'PUT' : 'POST'
      return apiFetch<ApiPolicySaveResponse>(path, {
        method,
        body: yaml,
        headers: { 'Content-Type': 'text/plain' },
      })
    },
    onSuccess: (_data, { id }) => {
      queryClient.invalidateQueries({ queryKey: POLICIES_QUERY_KEY })
      if (id) {
        queryClient.invalidateQueries({ queryKey: policyQueryKey(id) })
      }
    },
  })
}

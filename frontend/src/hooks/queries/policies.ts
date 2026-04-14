import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { apiFetch } from '@/api/fetch'
import { ApiError } from '@/api/fetch'
import type { ApiPolicyListItem, ApiPolicyDetail, WebhookSecretResponse } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function usePolicies() {
  return useQuery({
    queryKey: queryKeys.policies.all,
    queryFn: () => apiFetch<ApiPolicyListItem[]>('/policies'),
  })
}

export function usePolicy(id?: string) {
  return useQuery({
    queryKey: id ? queryKeys.policies.detail(id) : queryKeys.policies.detail(''),
    queryFn: () => apiFetch<ApiPolicyDetail>(`/policies/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
  })
}

// useWebhookSecret fetches the revealed plaintext webhook secret for a policy.
// enabled is false until the operator explicitly clicks "Show" — keeping the
// plaintext out of the cache for users who never reveal it.
//
// On 404 with error code "no_secret", resolves to null rather than throwing —
// "never rotated" is a UI state, not an error.
//
// staleTime and gcTime are both 0 so the plaintext does not survive in the
// cache longer than the component that requested it. On unmount, the query is
// removed from the cache entirely via a cleanup effect.
export function useWebhookSecret(policyId: string, enabled: boolean) {
  const queryClient = useQueryClient()
  const key = queryKeys.policies.webhookSecret(policyId)

  const query = useQuery({
    queryKey: key,
    queryFn: async () => {
      try {
        const data = await apiFetch<WebhookSecretResponse>(`/policies/${encodeURIComponent(policyId)}/webhook/secret`)
        return data.secret
      } catch (err) {
        if (err instanceof ApiError && err.status === 404 && err.message === 'no_secret') {
          return null
        }
        throw err
      }
    },
    enabled,
    staleTime: 0,
    gcTime: 0,
  })

  // Remove the cached plaintext from the query client when the component
  // unmounts, as an extra layer of protection beyond gcTime.
  useEffect(() => {
    return () => {
      queryClient.removeQueries({ queryKey: key })
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return query
}

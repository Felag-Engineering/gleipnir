import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, apiFetchVoid } from '@/api/fetch'
import type { ApiUserIdentity } from '@/api/types'
import { queryKeys } from '../queryKeys'

interface IdentityTarget {
  instanceId: string
}

interface SetIdentityParams extends IdentityTarget {
  externalUserId: string
}

// PUT /api/v1/users/{id}/identities/{instanceID}. A 409 means the actor id is
// already mapped to another user on that instance.
export function useSetUserIdentity(userId: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ instanceId, externalUserId }: SetIdentityParams) =>
      apiFetch<ApiUserIdentity>(
        `/users/${encodeURIComponent(userId)}/identities/${encodeURIComponent(instanceId)}`,
        { method: 'PUT', body: JSON.stringify({ external_user_id: externalUserId }) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.users.identities(userId) })
    },
  })
}

// DELETE /api/v1/users/{id}/identities/{instanceID}.
export function useClearUserIdentity(userId: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ instanceId }: IdentityTarget) =>
      apiFetchVoid(
        `/users/${encodeURIComponent(userId)}/identities/${encodeURIComponent(instanceId)}`,
        { method: 'DELETE' },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.users.identities(userId) })
    },
  })
}

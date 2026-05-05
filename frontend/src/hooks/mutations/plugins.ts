import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiAcceptNewKeyResponse } from '@/api/types'
import { queryKeys } from '../queryKeys'

interface AcceptNewKeyParams {
  pluginId: string
  candidatePubkey: string
}

// useAcceptPluginNewKey posts the candidate pubkey to the backend, which
// rotates the plugin's trusted_pubkey and unblocks any pending_key_approval
// instances. On success, the plugin instance query cache is invalidated so
// health chips refresh automatically.
export function useAcceptPluginNewKey() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ pluginId, candidatePubkey }: AcceptNewKeyParams) =>
      apiFetch<ApiAcceptNewKeyResponse>(`/admin/plugins/${encodeURIComponent(pluginId)}/accept-new-key`, {
        method: 'POST',
        body: JSON.stringify({ candidate_pubkey: candidatePubkey }),
      }),
    onSuccess: (_data, { pluginId }) => {
      // Invalidate all instance queries for this plugin so health chips reflect
      // the transition to healthy.
      void queryClient.invalidateQueries({
        queryKey: ['admin', 'plugins', pluginId],
      })
    },
  })
}

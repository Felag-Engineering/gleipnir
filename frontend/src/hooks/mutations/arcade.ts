import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, ApiError } from '@/api/fetch'
import type {
  ApiArcadeAuthorizeResponse,
  ArcadeAuthorizeRequest,
  ArcadeAuthorizeWaitRequest,
} from '@/api/types'
import { queryKeys } from '../queryKeys'

// useArcadeAuthorizeToolkit fires POST .../arcade/authorize for a single toolkit.
// Returns the immediate Arcade response: completed (no action needed) or pending
// (user must click through an OAuth URL). The component drives re-polling via
// useArcadeAuthorizeWait after opening the OAuth URL.
export function useArcadeAuthorizeToolkit(serverId: string) {
  const queryClient = useQueryClient()

  return useMutation<ApiArcadeAuthorizeResponse, ApiError, ArcadeAuthorizeRequest>({
    mutationFn: ({ toolkit }) =>
      apiFetch<ApiArcadeAuthorizeResponse>(
        `/mcp/servers/${encodeURIComponent(serverId)}/arcade/authorize`,
        { method: 'POST', body: JSON.stringify({ toolkit }) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.tools(serverId) })
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.toolsAll(serverId) })
    },
  })
}

// useArcadeAuthorizeWait fires POST .../arcade/authorize/wait with a given auth_id.
// The server long-polls Arcade for up to 10 seconds (safely under HTTP WriteTimeout).
// Each call returns either pending (frontend re-issues) or a terminal status.
export function useArcadeAuthorizeWait(serverId: string) {
  const queryClient = useQueryClient()

  return useMutation<ApiArcadeAuthorizeResponse, ApiError, ArcadeAuthorizeWaitRequest>({
    mutationFn: ({ toolkit, auth_id }) =>
      apiFetch<ApiArcadeAuthorizeResponse>(
        `/mcp/servers/${encodeURIComponent(serverId)}/arcade/authorize/wait`,
        { method: 'POST', body: JSON.stringify({ toolkit, auth_id }) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.tools(serverId) })
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.toolsAll(serverId) })
    },
  })
}

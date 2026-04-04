import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, apiFetchVoid, ApiError } from '@/api/fetch'
import type { ApiMcpServerCreateResponse, AddMcpServerRequest } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function useAddMcpServer() {
  const queryClient = useQueryClient()

  return useMutation<ApiMcpServerCreateResponse, ApiError, AddMcpServerRequest>({
    mutationFn: (params: AddMcpServerRequest) =>
      apiFetch<ApiMcpServerCreateResponse>('/mcp/servers', {
        method: 'POST',
        body: JSON.stringify(params),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.all })
    },
  })
}

export function useDeleteMcpServer() {
  const queryClient = useQueryClient()

  return useMutation<void, ApiError, string>({
    mutationFn: (serverId: string) =>
      apiFetchVoid(`/mcp/servers/${encodeURIComponent(serverId)}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.all })
    },
  })
}

interface ToolDiff {
  added: string[]
  removed: string[]
  modified: string[]
}

export function useDiscoverMcpServer() {
  const queryClient = useQueryClient()

  return useMutation<ToolDiff, ApiError, string>({
    mutationFn: (serverId: string) =>
      apiFetch<ToolDiff>(`/mcp/servers/${encodeURIComponent(serverId)}/discover`, {
        method: 'POST',
      }),
    onSuccess: (_data, serverId) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.tools(serverId) })
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.all })
    },
  })
}

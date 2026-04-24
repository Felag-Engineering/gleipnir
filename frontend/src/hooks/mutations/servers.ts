import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, apiFetchVoid, ApiError } from '@/api/fetch'
import type {
  ApiMcpServerCreateResponse,
  ApiMcpTool,
  AddMcpServerRequest,
  TestMcpConnectionRequest,
  TestMcpConnectionResponse,
} from '@/api/types'
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

// useTestMcpConnection performs an ephemeral MCP discovery handshake without
// persisting any data. No cache invalidation needed — this is a read-only probe.
export function useTestMcpConnection() {
  return useMutation<TestMcpConnectionResponse, ApiError, TestMcpConnectionRequest>({
    mutationFn: (params: TestMcpConnectionRequest) =>
      apiFetch<TestMcpConnectionResponse>('/mcp/servers/test', {
        method: 'POST',
        body: JSON.stringify(params),
      }),
  })
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
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.toolsAll(serverId) })
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.all })
    },
  })
}

export function useSetMcpToolEnabled() {
  const queryClient = useQueryClient()

  return useMutation<ApiMcpTool, ApiError, { serverId: string; toolId: string; enabled: boolean }>({
    mutationFn: ({ serverId, toolId, enabled }) =>
      apiFetch<ApiMcpTool>(
        `/mcp/servers/${encodeURIComponent(serverId)}/tools/${encodeURIComponent(toolId)}/enabled`,
        { method: 'PUT', body: JSON.stringify({ enabled }) },
      ),
    onSuccess: (_data, { serverId }) => {
      // Invalidate both cache keys so the Tools page (toolsAll) and the policy
      // form (tools) both reflect the updated enabled state.
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.tools(serverId) })
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.toolsAll(serverId) })
    },
  })
}

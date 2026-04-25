import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, apiFetchVoid, ApiError } from '@/api/fetch'
import type {
  ApiMcpServer,
  ApiMcpServerCreateResponse,
  AddMcpServerRequest,
  UpdateMcpServerRequest,
  SetMcpServerHeaderRequest,
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

export function useUpdateMcpServer() {
  const queryClient = useQueryClient()

  return useMutation<ApiMcpServer, ApiError, { id: string } & UpdateMcpServerRequest>({
    mutationFn: ({ id, ...params }) =>
      apiFetch<ApiMcpServer>(`/mcp/servers/${encodeURIComponent(id)}`, {
        method: 'PUT',
        body: JSON.stringify(params),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.all })
    },
  })
}

export function useSetMcpServerHeader() {
  const queryClient = useQueryClient()

  return useMutation<ApiMcpServer, ApiError, { id: string; name: string } & SetMcpServerHeaderRequest>({
    mutationFn: ({ id, name, value }) =>
      apiFetch<ApiMcpServer>(
        `/mcp/servers/${encodeURIComponent(id)}/headers/${encodeURIComponent(name)}`,
        { method: 'PUT', body: JSON.stringify({ value }) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.all })
    },
  })
}

export function useDeleteMcpServerHeader() {
  const queryClient = useQueryClient()

  return useMutation<ApiMcpServer, ApiError, { id: string; name: string }>({
    mutationFn: ({ id, name }) =>
      apiFetch<ApiMcpServer>(
        `/mcp/servers/${encodeURIComponent(id)}/headers/${encodeURIComponent(name)}`,
        { method: 'DELETE' },
      ),
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
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.all })
    },
  })
}

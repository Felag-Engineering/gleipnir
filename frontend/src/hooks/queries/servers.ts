import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function useMcpServers() {
  return useQuery({
    queryKey: queryKeys.servers.all,
    queryFn: () => apiFetch<ApiMcpServer[]>('/mcp/servers'),
    staleTime: 30_000,
  })
}

export function useMcpTools(serverId: string) {
  return useQuery({
    queryKey: queryKeys.servers.tools(serverId),
    queryFn: () => apiFetch<ApiMcpTool[]>(`/mcp/servers/${encodeURIComponent(serverId)}/tools`),
    enabled: Boolean(serverId),
    staleTime: 30_000,
  })
}

import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiMcpTool } from '../api/types'

export const mcpToolsQueryKey = (serverId: string) =>
  ['servers', serverId, 'tools'] as const

export function useMcpTools(serverId: string) {
  return useQuery({
    queryKey: mcpToolsQueryKey(serverId),
    queryFn: () => apiFetch<ApiMcpTool[]>(`/mcp/servers/${encodeURIComponent(serverId)}/tools`),
    enabled: Boolean(serverId),
  })
}

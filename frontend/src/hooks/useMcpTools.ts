import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiMcpTool } from '../api/types'
import { queryKeys } from './queryKeys'

export function useMcpTools(serverId: string) {
  return useQuery({
    queryKey: queryKeys.servers.tools(serverId),
    queryFn: () => apiFetch<ApiMcpTool[]>(`/mcp/servers/${encodeURIComponent(serverId)}/tools`),
    enabled: Boolean(serverId),
  })
}

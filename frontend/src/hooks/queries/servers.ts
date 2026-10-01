import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { queryKeys } from '../queryKeys'

// `enabled: false` skips the request — used where the current role's read
// would 403 (see permissions/roleAccess.ts).
export function useMcpServers({ enabled = true }: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: queryKeys.servers.all,
    queryFn: () => apiFetch<ApiMcpServer[]>('/mcp/servers'),
    staleTime: 30_000,
    enabled,
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

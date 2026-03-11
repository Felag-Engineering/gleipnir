import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiMcpServer } from '../api/types'

export const MCP_SERVERS_QUERY_KEY = ['servers'] as const

export function useMcpServers() {
  return useQuery({
    queryKey: MCP_SERVERS_QUERY_KEY,
    queryFn: () => apiFetch<ApiMcpServer[]>('/mcp/servers'),
  })
}

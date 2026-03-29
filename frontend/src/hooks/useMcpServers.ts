import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiMcpServer } from '@/api/types'
import { queryKeys } from './queryKeys'

export function useMcpServers() {
  return useQuery({
    queryKey: queryKeys.servers.all,
    queryFn: () => apiFetch<ApiMcpServer[]>('/mcp/servers'),
    staleTime: 30_000,
  })
}

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetchVoid } from '@/api/fetch'
import { MCP_SERVERS_QUERY_KEY } from './useMcpServers'

export function useDeleteMcpServer() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (serverId: string) =>
      apiFetchVoid(`/mcp/servers/${encodeURIComponent(serverId)}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: MCP_SERVERS_QUERY_KEY })
    },
  })
}

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import { queryKeys } from './queryKeys'

interface ToolDiff {
  added: string[]
  removed: string[]
  modified: string[]
}

export function useDiscoverMcpServer() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (serverId: string) =>
      apiFetch<ToolDiff>(`/mcp/servers/${encodeURIComponent(serverId)}/discover`, {
        method: 'POST',
      }),
    onSuccess: (_data, serverId) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.tools(serverId) })
    },
  })
}

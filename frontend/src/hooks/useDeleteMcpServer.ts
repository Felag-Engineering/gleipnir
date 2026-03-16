import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetchVoid } from '@/api/fetch'
import { queryKeys } from './queryKeys'

export function useDeleteMcpServer() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (serverId: string) =>
      apiFetchVoid(`/mcp/servers/${encodeURIComponent(serverId)}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.all })
    },
  })
}

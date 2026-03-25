import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiMcpTool } from '@/api/types'
import { queryKeys } from './queryKeys'

interface UpdateToolParams {
  toolId: string
  serverId: string
  capability_role: 'tool' | 'feedback'
}

export function useUpdateMcpTool() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ toolId, capability_role }: UpdateToolParams) =>
      apiFetch<ApiMcpTool>(`/mcp/tools/${encodeURIComponent(toolId)}`, {
        method: 'PATCH',
        body: JSON.stringify({ capability_role }),
      }),
    onSuccess: (_data, { serverId }) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.servers.tools(serverId) })
    },
  })
}

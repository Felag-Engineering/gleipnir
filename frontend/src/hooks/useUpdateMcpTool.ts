import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiMcpTool } from '@/api/types'
import { mcpToolsQueryKey } from './useMcpTools'

interface UpdateToolParams {
  toolId: string
  serverId: string
  capability_role: 'sensor' | 'actuator' | 'feedback'
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
      void queryClient.invalidateQueries({ queryKey: mcpToolsQueryKey(serverId) })
    },
  })
}

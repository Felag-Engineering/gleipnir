import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiMcpServerCreateResponse } from '@/api/types'
import { MCP_SERVERS_QUERY_KEY } from './useMcpServers'

interface AddServerParams {
  name: string
  url: string
}

export function useAddMcpServer() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (params: AddServerParams) =>
      apiFetch<ApiMcpServerCreateResponse>('/mcp/servers', {
        method: 'POST',
        body: JSON.stringify(params),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: MCP_SERVERS_QUERY_KEY })
    },
  })
}

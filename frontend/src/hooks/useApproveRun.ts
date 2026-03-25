import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import { queryKeys } from './queryKeys'

interface ApproveRunArgs {
  runId: string
  decision: 'approved' | 'denied'
}

interface ApproveRunResponse {
  run_id: string
  decision: string
}

export function useApproveRun() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ runId, decision }: ApproveRunArgs) =>
      apiFetch<ApproveRunResponse>(`/runs/${encodeURIComponent(runId)}/approval`, {
        method: 'POST',
        body: JSON.stringify({ decision }),
        headers: { 'Content-Type': 'application/json' },
      }),
    onSuccess: (_data, { runId }) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.detail(runId) })
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.steps(runId) })
      queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.approvals.all })
    },
  })
}

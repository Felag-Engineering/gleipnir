import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import { queryKeys } from './queryKeys'

interface SubmitFeedbackArgs {
  runId: string
  response: string
  feedbackId?: string
}

interface SubmitFeedbackResponse {
  run_id: string
}

export function useSubmitFeedback() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ runId, response, feedbackId }: SubmitFeedbackArgs) =>
      apiFetch<SubmitFeedbackResponse>(`/runs/${encodeURIComponent(runId)}/feedback`, {
        method: 'POST',
        body: JSON.stringify({ response, ...(feedbackId ? { feedback_id: feedbackId } : {}) }),
        headers: { 'Content-Type': 'application/json' },
      }),
    onSuccess: (_data, { runId }) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.detail(runId) })
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.steps(runId) })
      queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
    },
  })
}

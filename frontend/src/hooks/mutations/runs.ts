import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApproveRunRequest, ApproveRunResponse, SubmitFeedbackRequest, SubmitFeedbackResponse } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function useApproveRun() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ runId, decision }: ApproveRunRequest) =>
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

export function useSubmitFeedback() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ runId, response, feedbackId }: SubmitFeedbackRequest) =>
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

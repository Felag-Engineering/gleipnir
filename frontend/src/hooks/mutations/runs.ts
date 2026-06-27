import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApproveRunRequest, ApproveRunResponse, CancelRunResponse, SubmitFeedbackRequest, SubmitFeedbackResponse } from '@/api/types'
import { queryKeys } from '../queryKeys'

// useCancelRun cancels an in-flight run (pending / running / waiting_for_*).
// The backend transitions it to failed and unblocks any approval/feedback gate
// (POST /runs/{id}/cancel, operator|admin). 409 if the run is already terminal.
export function useCancelRun() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (runId: string) =>
      apiFetch<CancelRunResponse>(`/runs/${encodeURIComponent(runId)}/cancel`, {
        method: 'POST',
      }),
    onSuccess: (_data, runId) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.detail(runId) })
      queryClient.invalidateQueries({ queryKey: queryKeys.runs.steps(runId) })
      queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.attention.all })
    },
  })
}

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

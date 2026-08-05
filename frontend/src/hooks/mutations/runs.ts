import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiToolInputResponseItem, ApproveRunRequest, ApproveRunResponse, CancelRunResponse, SubmitFeedbackRequest, SubmitFeedbackResponse } from '@/api/types'
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

// invalidateToolInputViews refreshes everything a resolved tool-initiated
// request changes: the run itself, its trace, and the attention queue that was
// showing it as waiting on somebody.
function invalidateToolInputViews(queryClient: ReturnType<typeof useQueryClient>, runId: string) {
  queryClient.invalidateQueries({ queryKey: queryKeys.runs.toolInput(runId) })
  queryClient.invalidateQueries({ queryKey: queryKeys.runs.detail(runId) })
  queryClient.invalidateQueries({ queryKey: queryKeys.runs.steps(runId) })
  queryClient.invalidateQueries({ queryKey: queryKeys.runs.decisions(runId) })
  queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })
  queryClient.invalidateQueries({ queryKey: queryKeys.attention.all })
  queryClient.invalidateQueries({ queryKey: queryKeys.stats.all })
}

// useSubmitToolInput answers a tool-initiated request (POST
// /runs/{id}/tool-input, ADR-055 §6.1).
//
// Responses are correlated to the request's questions BY POSITION — MRTR
// carries no per-question id — so the caller must send exactly one entry per
// question, in order. The backend rejects a mismatch rather than padding it,
// because answering the wrong question is worse than answering none.
//
// A decline is a real answer, not a cancellation: MRTR hands the refusal back
// to the server, which decides what to do with it.
export function useSubmitToolInput() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ runId, responses }: { runId: string; responses: ApiToolInputResponseItem[] }) =>
      apiFetch<{ run_id: string; request_id: string }>(
        `/runs/${encodeURIComponent(runId)}/tool-input`,
        {
          method: 'POST',
          body: JSON.stringify({ responses }),
          headers: { 'Content-Type': 'application/json' },
        },
      ),
    onSuccess: (_data, { runId }) => invalidateToolInputViews(queryClient, runId),
  })
}

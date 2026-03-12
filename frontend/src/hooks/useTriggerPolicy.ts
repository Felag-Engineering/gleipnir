import { useMutation } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'

interface TriggerPolicyArgs {
  policyId: string
  message?: string
}

export interface TriggerPolicyResponse {
  run_id: string
}

export function useTriggerPolicy() {
  return useMutation({
    mutationFn: ({ policyId, message }: TriggerPolicyArgs) => {
      const body = message ? JSON.stringify({ message }) : '{}'
      return apiFetch<TriggerPolicyResponse>(`/policies/${encodeURIComponent(policyId)}/trigger`, {
        method: 'POST',
        body,
        headers: { 'Content-Type': 'application/json' },
      })
    },
  })
}

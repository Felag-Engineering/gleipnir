import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, apiFetchVoid } from '@/api/fetch'
import type { ApiPolicyDetail, ApiPolicySaveResponse, TriggerPolicyRequest, TriggerPolicyResponse } from '@/api/types'
import { queryKeys } from '../queryKeys'

interface SavePolicyArgs {
  id?: string   // absent → POST (create), present → PUT (update)
  yaml: string  // raw YAML string sent as text/plain body
}

export function useSavePolicy() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ id, yaml }: SavePolicyArgs) => {
      const path = id ? `/policies/${encodeURIComponent(id)}` : '/policies'
      const method = id ? 'PUT' : 'POST'
      return apiFetch<ApiPolicySaveResponse>(path, {
        method,
        body: yaml,
        headers: { 'Content-Type': 'text/plain' },
      })
    },
    onSuccess: (_data, { id }) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })
      if (id) {
        queryClient.invalidateQueries({ queryKey: queryKeys.policies.detail(id) })
      }
    },
  })
}

export function useDeletePolicy() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: string) =>
      apiFetchVoid(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })
    },
  })
}

export function usePausePolicy() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<ApiPolicyDetail>(`/policies/${encodeURIComponent(id)}/pause`, { method: 'POST' }),
    onSuccess: (_data, id) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.policies.detail(id) })
    },
  })
}

export function useResumePolicy() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<ApiPolicyDetail>(`/policies/${encodeURIComponent(id)}/resume`, { method: 'POST' }),
    onSuccess: (_data, id) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })
      queryClient.invalidateQueries({ queryKey: queryKeys.policies.detail(id) })
    },
  })
}

export function useTriggerPolicy() {
  return useMutation({
    mutationFn: ({ policyId, message }: TriggerPolicyRequest) => {
      const body = message ? JSON.stringify({ message }) : '{}'
      return apiFetch<TriggerPolicyResponse>(`/policies/${encodeURIComponent(policyId)}/trigger`, {
        method: 'POST',
        body,
        headers: { 'Content-Type': 'application/json' },
      })
    },
  })
}

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, apiFetchVoid } from '@/api/fetch'
import type { ApiAudience, AudienceCreateRequest, AudienceUpdateRequest } from '@/api/types'
import { queryKeys } from '../queryKeys'

export function useSetProviderKey() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ provider, key }: { provider: string; key: string }) =>
      apiFetch<{ status: string }>(`/admin/providers/${encodeURIComponent(provider)}/key`, {
        method: 'PUT',
        body: JSON.stringify({ key }),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.providers })
      void queryClient.invalidateQueries({ queryKey: queryKeys.models.all })
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.modelsAll })
    },
  })
}

export function useDeleteProviderKey() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (provider: string) =>
      apiFetch<{ status: string }>(`/admin/providers/${encodeURIComponent(provider)}/key`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.providers })
      void queryClient.invalidateQueries({ queryKey: queryKeys.models.all })
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.modelsAll })
    },
  })
}

export function useUpdateAdminSettings() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (settings: Record<string, string>) =>
      apiFetch<{ status: string }>('/admin/settings', {
        method: 'PUT',
        body: JSON.stringify(settings),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.settings })
    },
  })
}

export function useSetModelEnabled() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ modelId, provider, enabled }: { modelId: string; provider: string; enabled: boolean }) =>
      apiFetch<{ status: string }>(`/admin/models/${encodeURIComponent(modelId)}/enabled`, {
        method: 'PUT',
        body: JSON.stringify({ provider, enabled }),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.models })
      void queryClient.invalidateQueries({ queryKey: queryKeys.models.all })
    },
  })
}

export function useCreateAudience() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (req: AudienceCreateRequest) =>
      apiFetch<ApiAudience>('/admin/audiences', {
        method: 'POST',
        body: JSON.stringify(req),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.audiences })
    },
  })
}

export function useUpdateAudience() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, req }: { id: string; req: AudienceUpdateRequest }) =>
      apiFetch<ApiAudience>(`/admin/audiences/${encodeURIComponent(id)}`, {
        method: 'PUT',
        body: JSON.stringify(req),
      }),
    onSuccess: (_data, { id }) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.audiences })
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.audienceDetail(id) })
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.audienceReferences(id) })
    },
  })
}

export function useDeleteAudience() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) =>
      apiFetchVoid(`/admin/audiences/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.audiences })
    },
  })
}

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
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

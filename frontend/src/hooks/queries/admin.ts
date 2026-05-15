import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type {
  ApiProviderStatus,
  ApiModelSetting,
  ApiAllModelEntry,
  ApiSystemSettings,
  ApiSystemInfo,
  ApiAudienceListItem,
  ApiAudience,
  ApiAudienceReferences,
  ApiPluginInstanceForAudience,
} from '@/api/types'
import { queryKeys } from '../queryKeys'

export function useProviders() {
  return useQuery({
    queryKey: queryKeys.admin.providers,
    queryFn: () => apiFetch<ApiProviderStatus[]>('/admin/providers'),
  })
}

export function useAdminModels() {
  return useQuery({
    queryKey: queryKeys.admin.models,
    queryFn: () => apiFetch<ApiModelSetting[]>('/admin/models'),
  })
}

export function useAllAdminModels() {
  return useQuery({
    queryKey: queryKeys.admin.modelsAll,
    queryFn: () => apiFetch<ApiAllModelEntry[]>('/admin/models/all'),
  })
}

export function useAdminSettings() {
  return useQuery({
    queryKey: queryKeys.admin.settings,
    queryFn: () => apiFetch<ApiSystemSettings>('/admin/settings'),
  })
}

export function useSystemInfo() {
  return useQuery({
    queryKey: queryKeys.admin.systemInfo,
    queryFn: () => apiFetch<ApiSystemInfo>('/admin/system-info'),
    refetchInterval: 30_000,
    refetchOnWindowFocus: true,
  })
}

export function useAudiences() {
  return useQuery({
    queryKey: queryKeys.admin.audiences,
    queryFn: () => apiFetch<ApiAudienceListItem[]>('/admin/audiences'),
  })
}

export function useAudience(id: string | undefined) {
  return useQuery({
    queryKey: queryKeys.admin.audienceDetail(id ?? ''),
    queryFn: () => apiFetch<ApiAudience>(`/admin/audiences/${encodeURIComponent(id!)}`),
    enabled: !!id,
  })
}

export function useAudienceReferences(id: string | undefined) {
  return useQuery({
    queryKey: queryKeys.admin.audienceReferences(id ?? ''),
    queryFn: () =>
      apiFetch<ApiAudienceReferences>(`/admin/audiences/${encodeURIComponent(id!)}/references`),
    enabled: !!id,
  })
}

// GET /api/v1/admin/plugin-instances — consumed by the audience editor (#290)
// and the admin/plugins page (#230).
export function usePluginInstancesForAudience() {
  return useQuery({
    queryKey: queryKeys.admin.pluginInstances,
    queryFn: () => apiFetch<ApiPluginInstanceForAudience[]>('/admin/plugin-instances'),
  })
}

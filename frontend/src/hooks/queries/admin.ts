import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiProviderStatus, ApiModelSetting, ApiAllModelEntry, ApiSystemSettings, ApiSystemInfo } from '@/api/types'
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

import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiPluginOptionsResponse } from '@/api/types'
import { queryKeys } from './queryKeys'

export interface UsePluginOptionsParams {
  pluginId: string
  instanceId: string
  source: string
  query?: string
  cursor?: string
  // enabled controls whether the query fires at all. Pass false when the
  // combobox has not yet been opened or when the instance is not known.
  enabled?: boolean
}

// usePluginOptions fetches dynamic option lists from a plugin's
// ConfigOptionsService. Results are cached per (pluginId, instanceId, source,
// query, cursor) by TanStack Query with a 30s staleTime to avoid hammering the
// plugin subprocess on every keystroke.
//
// When the plugin returns degraded:true (no provider or unhealthy instance),
// the hook surfaces it via data.degraded so the calling component can fall back
// to a plain text input.
export function usePluginOptions({
  pluginId,
  instanceId,
  source,
  query = '',
  cursor = '',
  enabled = true,
}: UsePluginOptionsParams) {
  return useQuery({
    queryKey: queryKeys.plugins.options(pluginId, instanceId, source, query, cursor),
    queryFn: () => {
      const params = new URLSearchParams()
      if (query) params.set('query', query)
      if (cursor) params.set('cursor', cursor)
      const qs = params.toString()
      const path =
        `/admin/plugins/${encodeURIComponent(pluginId)}/instances/${encodeURIComponent(instanceId)}/options/${encodeURIComponent(source)}` +
        (qs ? `?${qs}` : '')
      return apiFetch<ApiPluginOptionsResponse>(path)
    },
    enabled: enabled && Boolean(pluginId) && Boolean(instanceId) && Boolean(source),
    // 30s stale time: the plugin options endpoint already caches at the server
    // level for 30s; align TanStack's client cache with that TTL so we don't
    // refetch on every mount within the same window.
    staleTime: 30_000,
  })
}

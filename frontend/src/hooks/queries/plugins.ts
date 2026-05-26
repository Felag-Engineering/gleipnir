import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type {
  ApiPluginListItem,
  ApiPluginDetail,
  ApiRedactedCredentials,
} from '@/api/types'
import { queryKeys } from '../queryKeys'

// usePlugins fetches all installed plugins (pending_review + active).
// Used by AdminPluginsPage to show the pending-review section.
export function usePlugins() {
  return useQuery({
    queryKey: queryKeys.plugins.list(),
    queryFn: () => apiFetch<ApiPluginListItem[]>('/admin/plugins'),
  })
}

// usePluginDetail fetches the full detail for a single plugin.
// Used by PluginReviewPage to render the consent surface.
export function usePluginDetail(pluginId: string | undefined) {
  return useQuery({
    queryKey: queryKeys.plugins.detail(pluginId ?? ''),
    queryFn: () =>
      apiFetch<ApiPluginDetail>(
        `/admin/plugins/${encodeURIComponent(pluginId!)}`,
      ),
    enabled: !!pluginId,
  })
}

// usePluginInstanceCredentials fetches the redacted credential view for a
// plugin instance. Secret values are never included — only presence flags and
// non-secret metadata (see ApiRedactedCredentials / oauth.RedactedCredentials).
export function usePluginInstanceCredentials(
  pluginId: string | undefined,
  instanceId: string | undefined,
) {
  return useQuery({
    queryKey: queryKeys.plugins.credentials(pluginId ?? '', instanceId ?? ''),
    queryFn: () =>
      apiFetch<ApiRedactedCredentials>(
        `/admin/plugins/${encodeURIComponent(pluginId!)}/instances/${encodeURIComponent(instanceId!)}/credentials`,
      ),
    enabled: !!pluginId && !!instanceId,
  })
}

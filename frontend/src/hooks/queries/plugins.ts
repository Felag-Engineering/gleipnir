import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiRedactedCredentials } from '@/api/types'
import { queryKeys } from '../queryKeys'

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

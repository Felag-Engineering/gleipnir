import { useState, useCallback } from 'react'
import { apiFetch } from '@/api/fetch'
import type { ApiPluginOptionsResponse } from '@/api/types'
import type { OptionsContext } from '@/components/form/SchemaForm'

// useOptionsContext returns an OptionsContext that calls the plugin options
// endpoint via apiFetch (bypasses TanStack Query — the server-side 30s cache
// handles request deduplication). The `degraded` flag is tracked from the
// latest successful response so AsyncCombobox can fall back to free-text
// input when the plugin's ConfigOptionsService is unavailable.
export function useOptionsContext(pluginId: string, instanceId: string): OptionsContext {
  const [degraded, setDegraded] = useState<boolean | undefined>(undefined)

  const search = useCallback(
    async (source: string, query: string) => {
      const params = new URLSearchParams()
      if (query) params.set('query', query)
      const qs = params.toString()
      const path =
        `/admin/plugins/${encodeURIComponent(pluginId)}/instances/${encodeURIComponent(instanceId)}/options/${encodeURIComponent(source)}` +
        (qs ? `?${qs}` : '')
      try {
        const res = await apiFetch<ApiPluginOptionsResponse>(path)
        // Surface the degraded flag from the response so the combobox can
        // fall back to free-text when the host returns degraded:true.
        setDegraded(res.degraded ?? false)
        return res.options ?? []
      } catch {
        // On network/auth error, mark degraded so the user can still type a value.
        setDegraded(true)
        return []
      }
    },
    [pluginId, instanceId],
  )
  return { search, degraded }
}

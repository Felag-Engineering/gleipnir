import { useMutation } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiBindingTestRequest, ApiBindingTestResponse } from '@/api/types'

// useTestBindingAgainstSamples sends the operator's current binding + a set of
// example payloads to the server-side evaluator and returns per-example
// match/no-match results. The server uses Go RE2 semantics, keeping preview
// results consistent with the runtime evaluator.
//
// No cache invalidation: this is a read-only operation.
export function useTestBindingAgainstSamples(instanceId: string, eventKind: string) {
  return useMutation({
    mutationFn: (req: ApiBindingTestRequest) =>
      apiFetch<ApiBindingTestResponse>(
        `/admin/plugin-instances/${encodeURIComponent(instanceId)}/event-kinds/${encodeURIComponent(eventKind)}/test-binding`,
        {
          method: 'POST',
          body: JSON.stringify(req),
        },
      ),
  })
}

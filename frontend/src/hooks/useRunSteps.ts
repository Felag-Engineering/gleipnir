import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import type { ApiRunStep } from '../api/types'

export function useRunSteps(id?: string) {
  return useQuery({
    queryKey: ['runs', id, 'steps'] as const,
    queryFn: () => apiFetch<ApiRunStep[]>(`/runs/${encodeURIComponent(id!)}/steps`),
    enabled: Boolean(id),
  })
}

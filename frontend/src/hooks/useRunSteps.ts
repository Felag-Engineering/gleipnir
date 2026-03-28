import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import type { ApiRunStep } from '@/api/types'
import { queryKeys } from './queryKeys'

export function useRunSteps(id?: string) {
  return useQuery({
    queryKey: queryKeys.runs.steps(id ?? ''),
    queryFn: () => apiFetch<ApiRunStep[]>(`/runs/${encodeURIComponent(id!)}/steps`),
    enabled: Boolean(id),
  })
}

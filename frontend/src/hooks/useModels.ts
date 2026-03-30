import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/api/fetch'
import { queryKeys } from './queryKeys'

interface ModelInfo {
  name: string
  display_name: string
}

export interface ProviderModels {
  provider: string
  models: ModelInfo[]
}

export function useModels() {
  return useQuery({
    queryKey: queryKeys.models.all,
    queryFn: () => apiFetch<ProviderModels[]>('/models'),
    staleTime: 5 * 60 * 1000, // models don't change often, cache 5 min
  })
}

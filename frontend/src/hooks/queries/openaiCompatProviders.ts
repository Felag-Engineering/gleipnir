import { useQuery } from '@tanstack/react-query'
import { listOpenAICompatProviders } from '@/api/openaiCompatProviders'
import { queryKeys } from '../queryKeys'

export function useOpenAICompatProviders() {
  return useQuery({
    queryKey: queryKeys.admin.openaiCompatProviders,
    queryFn: listOpenAICompatProviders,
  })
}

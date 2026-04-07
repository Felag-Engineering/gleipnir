import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  createOpenAICompatProvider,
  updateOpenAICompatProvider,
  deleteOpenAICompatProvider,
  testOpenAICompatProvider,
} from '@/api/openaiCompatProviders'
import type { ApiOpenAICompatProviderUpsert } from '@/api/types'
import { queryKeys } from '../queryKeys'

function invalidateProviderCaches(queryClient: ReturnType<typeof useQueryClient>) {
  void queryClient.invalidateQueries({ queryKey: queryKeys.admin.openaiCompatProviders })
  void queryClient.invalidateQueries({ queryKey: queryKeys.models.all })
}

export function useCreateOpenAICompatProvider() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: ApiOpenAICompatProviderUpsert) => createOpenAICompatProvider(body),
    onSuccess: () => invalidateProviderCaches(queryClient),
  })
}

export function useUpdateOpenAICompatProvider() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: ApiOpenAICompatProviderUpsert }) =>
      updateOpenAICompatProvider(id, body),
    onSuccess: () => invalidateProviderCaches(queryClient),
  })
}

export function useDeleteOpenAICompatProvider() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => deleteOpenAICompatProvider(id),
    onSuccess: () => invalidateProviderCaches(queryClient),
  })
}

export function useTestOpenAICompatProvider() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => testOpenAICompatProvider(id),
    onSuccess: () => invalidateProviderCaches(queryClient),
  })
}

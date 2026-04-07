import { apiFetch, apiFetchVoid } from './fetch'
import type {
  ApiOpenAICompatProvider,
  ApiOpenAICompatProviderTestResult,
  ApiOpenAICompatProviderUpsert,
} from './types'

const BASE = '/admin/openai-providers'

export function listOpenAICompatProviders(): Promise<ApiOpenAICompatProvider[]> {
  return apiFetch<ApiOpenAICompatProvider[]>(BASE)
}

export function getOpenAICompatProvider(id: number): Promise<ApiOpenAICompatProvider> {
  return apiFetch<ApiOpenAICompatProvider>(`${BASE}/${id}`)
}

export function createOpenAICompatProvider(
  body: ApiOpenAICompatProviderUpsert,
): Promise<ApiOpenAICompatProvider> {
  return apiFetch<ApiOpenAICompatProvider>(BASE, { method: 'POST', body: JSON.stringify(body) })
}

export function updateOpenAICompatProvider(
  id: number,
  body: ApiOpenAICompatProviderUpsert,
): Promise<ApiOpenAICompatProvider> {
  return apiFetch<ApiOpenAICompatProvider>(`${BASE}/${id}`, {
    method: 'PUT',
    body: JSON.stringify(body),
  })
}

export function deleteOpenAICompatProvider(id: number): Promise<void> {
  return apiFetchVoid(`${BASE}/${id}`, { method: 'DELETE' })
}

export function testOpenAICompatProvider(id: number): Promise<ApiOpenAICompatProviderTestResult> {
  return apiFetch<ApiOpenAICompatProviderTestResult>(`${BASE}/${id}/test`, { method: 'POST' })
}

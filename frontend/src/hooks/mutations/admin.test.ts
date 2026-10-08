import { describe, it, expect, vi } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import React from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { queryKeys } from '@/hooks/queryKeys'
import { useSetProviderKey } from './admin'

describe('useSetProviderKey', () => {
  it('invalidates the admin settings and public config queries so the seeded default shows up', async () => {
    server.use(
      http.put('/api/v1/admin/providers/anthropic/key', () =>
        HttpResponse.json({ data: { status: 'ok' } }),
      ),
    )
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const wrapper = ({ children }: { children: React.ReactNode }) =>
      React.createElement(QueryClientProvider, { client }, children)

    const { result } = renderHook(() => useSetProviderKey(), { wrapper })
    await act(async () => {
      await result.current.mutateAsync({ provider: 'anthropic', key: 'sk-test' })
    })

    const keys = invalidate.mock.calls.map((c) => c[0]?.queryKey)
    expect(keys).toContainEqual(queryKeys.admin.settings)
    expect(keys).toContainEqual(queryKeys.config.all)
  })
})

import { describe, it, expect } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { createElement } from 'react'
import { server } from '@/test/server'
import { useSetupReadiness } from './useSetupReadiness'

function makeWrapper() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return ({ children }: { children: React.ReactNode }) =>
    createElement(QueryClientProvider, { client: queryClient }, children)
}

function setupHandlers(opts: {
  models?: { provider: string; models: { name: string; display_name: string }[] }[]
  servers?: { id: string; name: string }[]
  policies?: { id: string; name: string }[]
}) {
  server.use(
    http.get('/api/v1/models', () =>
      HttpResponse.json({ data: opts.models ?? [] }),
    ),
    http.get('/api/v1/mcp/servers', () =>
      HttpResponse.json({ data: opts.servers ?? [] }),
    ),
    http.get('/api/v1/policies', () =>
      HttpResponse.json({ data: opts.policies ?? [] }),
    ),
  )
}

describe('useSetupReadiness', () => {
  it('returns nextStep model when everything is empty', async () => {
    setupHandlers({})

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.hasModel).toBe(false)
    expect(result.current.hasServer).toBe(false)
    expect(result.current.hasAgent).toBe(false)
    expect(result.current.nextStep).toBe('model')
  })

  it('returns nextStep model when provider group exists but has no models (no API key configured)', async () => {
    setupHandlers({ models: [{ provider: 'anthropic', models: [] }] })

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.hasModel).toBe(false)
    expect(result.current.nextStep).toBe('model')
  })

  it('returns nextStep server when models are present but no servers', async () => {
    setupHandlers({
      models: [{ provider: 'anthropic', models: [{ name: 'claude-3', display_name: 'Claude 3' }] }],
    })

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.hasModel).toBe(true)
    expect(result.current.hasServer).toBe(false)
    expect(result.current.nextStep).toBe('server')
  })

  it('returns nextStep agent when models and servers are present but no agents', async () => {
    setupHandlers({
      models: [{ provider: 'anthropic', models: [{ name: 'claude-3', display_name: 'Claude 3' }] }],
      servers: [{ id: 's1', name: 'my-server' }],
    })

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.hasModel).toBe(true)
    expect(result.current.hasServer).toBe(true)
    expect(result.current.hasAgent).toBe(false)
    expect(result.current.nextStep).toBe('agent')
  })

  it('returns nextStep ready when all three are present', async () => {
    setupHandlers({
      models: [{ provider: 'anthropic', models: [{ name: 'claude-3', display_name: 'Claude 3' }] }],
      servers: [{ id: 's1', name: 'my-server' }],
      policies: [{ id: 'p1', name: 'my-agent' }],
    })

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.hasModel).toBe(true)
    expect(result.current.hasServer).toBe(true)
    expect(result.current.hasAgent).toBe(true)
    expect(result.current.nextStep).toBe('ready')
  })

  it('reports isLoading true while queries are in flight', () => {
    setupHandlers({})

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    expect(result.current.isLoading).toBe(true)
  })

  it('reports isError true when a query fails', async () => {
    server.use(
      http.get('/api/v1/models', () => HttpResponse.json({ error: 'server error' }, { status: 500 })),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/policies', () => HttpResponse.json({ data: [] })),
    )

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.isError).toBe(true)
  })
})

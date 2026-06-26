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
  plugins?: { id: string; instance_name: string; services?: string[] }[]
  pluginsStatus?: number
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
    http.get('/api/v1/admin/plugin-instances', () => {
      if (opts.pluginsStatus && opts.pluginsStatus !== 200) {
        return HttpResponse.json({ error: 'forbidden' }, { status: opts.pluginsStatus })
      }
      return HttpResponse.json({ data: opts.plugins ?? [] })
    }),
  )
}

describe('useSetupReadiness', () => {
  it('returns nextStep model when everything is empty', async () => {
    setupHandlers({})

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.hasModel).toBe(false)
    expect(result.current.hasToolSource).toBe(false)
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

  it('returns nextStep tools when models are present but no servers or tool plugins', async () => {
    setupHandlers({
      models: [{ provider: 'anthropic', models: [{ name: 'claude-3', display_name: 'Claude 3' }] }],
    })

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.hasModel).toBe(true)
    expect(result.current.hasToolSource).toBe(false)
    expect(result.current.nextStep).toBe('tools')
  })

  it('returns nextStep agent when models and an MCP server are present but no agents', async () => {
    setupHandlers({
      models: [{ provider: 'anthropic', models: [{ name: 'claude-3', display_name: 'Claude 3' }] }],
      servers: [{ id: 's1', name: 'my-server' }],
    })

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.hasModel).toBe(true)
    expect(result.current.hasToolSource).toBe(true)
    expect(result.current.hasAgent).toBe(false)
    expect(result.current.nextStep).toBe('agent')
  })

  it('returns nextStep agent when models and a tool plugin are present but no servers and no agents', async () => {
    setupHandlers({
      models: [{ provider: 'anthropic', models: [{ name: 'claude-3', display_name: 'Claude 3' }] }],
      plugins: [{ id: 'pi1', instance_name: 'slack-main', services: ['tool', 'channel'] }],
    })

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.hasModel).toBe(true)
    expect(result.current.hasToolSource).toBe(true)
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
    expect(result.current.hasToolSource).toBe(true)
    expect(result.current.hasAgent).toBe(true)
    expect(result.current.nextStep).toBe('ready')
  })

  it('reports isLoading true while queries are in flight', () => {
    setupHandlers({})

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    expect(result.current.isLoading).toBe(true)
  })

  it('reports isError true when a query fails (models 500), plugin 403 does not contribute', async () => {
    server.use(
      http.get('/api/v1/models', () => HttpResponse.json({ error: 'server error' }, { status: 500 })),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/policies', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.isError).toBe(true)
  })

  it('degrades gracefully when plugin-instances returns 403 — isError stays false, hasToolSource stays false', async () => {
    server.use(
      http.get('/api/v1/models', () =>
        HttpResponse.json({ data: [{ provider: 'anthropic', models: [{ name: 'claude-3', display_name: 'Claude 3' }] }] }),
      ),
      http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/policies', () => HttpResponse.json({ data: [] })),
      http.get('/api/v1/admin/plugin-instances', () =>
        HttpResponse.json({ error: 'forbidden' }, { status: 403 }),
      ),
    )

    const { result } = renderHook(() => useSetupReadiness(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.isError).toBe(false)
    expect(result.current.hasToolSource).toBe(false)
    expect(result.current.nextStep).toBe('tools')
  })
})

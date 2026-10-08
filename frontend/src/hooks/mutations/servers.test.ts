import { describe, it, expect } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'
import React from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { queryKeys } from '@/hooks/queryKeys'
import { useMcpServers, useMcpTools } from '@/hooks/queries/servers'
import { useDeleteMcpServer } from './servers'

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return {
    client,
    wrapper({ children }: { children: React.ReactNode }) {
      return React.createElement(QueryClientProvider, { client }, children)
    },
  }
}

describe('useDeleteMcpServer', () => {
  it('removes the deleted server tools queries without refetching them, and refetches the list', async () => {
    let toolsFetches = 0
    let listFetches = 0
    let deleted = false
    server.use(
      http.get('/api/v1/mcp/servers', () => {
        listFetches++
        return HttpResponse.json({ data: deleted ? [] : [{ id: 'srv1' }] })
      }),
      http.get('/api/v1/mcp/servers/srv1/tools', () => {
        toolsFetches++
        return HttpResponse.json({ data: [] })
      }),
      http.delete('/api/v1/mcp/servers/srv1', () => {
        deleted = true
        return new HttpResponse(null, { status: 204 })
      }),
    )

    const { client, wrapper } = makeWrapper()
    // Like the Tools page, only observe a server's tools while the list shows it.
    const { result } = renderHook(
      () => {
        const list = useMcpServers()
        const stillListed = list.data?.some((s) => s.id === 'srv1') ?? false
        return { list, tools: useMcpTools(stillListed ? 'srv1' : ''), del: useDeleteMcpServer() }
      },
      { wrapper },
    )
    await waitFor(() => {
      expect(result.current.list.isSuccess).toBe(true)
      expect(result.current.tools.isSuccess).toBe(true)
    })
    client.setQueryData(queryKeys.servers.toolsAll('srv1'), [])
    expect(toolsFetches).toBe(1)
    expect(listFetches).toBe(1)

    await act(async () => {
      await result.current.del.mutateAsync({ id: 'srv1' })
    })

    await waitFor(() => expect(listFetches).toBe(2))
    expect(client.getQueryData(queryKeys.servers.toolsAll('srv1'))).toBeUndefined()
    expect(toolsFetches).toBe(1)
  })
})

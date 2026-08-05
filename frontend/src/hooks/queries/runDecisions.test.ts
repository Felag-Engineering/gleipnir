import { describe, it, expect } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import React from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { useRunDecisions } from './runs'
import type { ApiRunDecision } from '@/api/types'

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return {
    client,
    wrapper({ children }: { children: React.ReactNode }) {
      return React.createElement(QueryClientProvider, { client }, children)
    },
  }
}

function makeDecision(overrides: Partial<ApiRunDecision> = {}): ApiRunDecision {
  return {
    run_id: 'run1',
    request_id: 'req-1',
    type: 'tool_permission_request',
    kind: 'permission',
    severity: 'info',
    tool_name: 'deploy.release',
    channel_entry_id: 'entry-1',
    channel_assurance: 'authenticated',
    actor_external_id: 'U123',
    actor_user_id: 'user-7',
    link_method: 'directory_mapping',
    link_verified: true,
    outcome: 'answered',
    decided_at: '2026-08-05T12:00:00Z',
    ...overrides,
  }
}

describe('useRunDecisions', () => {
  it('loads a run’s decision records', async () => {
    const decisions = [makeDecision()]
    let capturedUrl = ''

    server.use(
      http.get('/api/v1/runs/run1/decisions', ({ request }) => {
        capturedUrl = request.url
        return HttpResponse.json({ data: decisions })
      }),
    )

    const { wrapper } = makeWrapper()
    const { result } = renderHook(() => useRunDecisions('run1'), { wrapper })

    await waitFor(() => expect(result.current.status).toBe('success'))
    expect(capturedUrl).toContain('/api/v1/runs/run1/decisions')
    expect(result.current.decisions).toHaveLength(1)
    expect(result.current.decisions[0].request_id).toBe('req-1')
  })

  // The actor ID travels with the verification flag so a renderer can never
  // show one without the other: an unverified ID is the channel's claim about
  // who acted, not an identity the host stands behind.
  it('carries an unverified actor alongside its link method', async () => {
    server.use(
      http.get('/api/v1/runs/run1/decisions', () =>
        HttpResponse.json({
          data: [
            makeDecision({
              severity: 'warning',
              actor_external_id: 'someone@example.com',
              actor_user_id: undefined,
              link_method: 'unverified',
              link_verified: false,
            }),
          ],
        }),
      ),
    )

    const { wrapper } = makeWrapper()
    const { result } = renderHook(() => useRunDecisions('run1'), { wrapper })

    await waitFor(() => expect(result.current.status).toBe('success'))
    const record = result.current.decisions[0]
    expect(record.link_verified).toBe(false)
    expect(record.actor_user_id).toBeUndefined()
    expect(record.severity).toBe('warning')
  })

  it('does not fetch without a run id', () => {
    const { wrapper } = makeWrapper()
    const { result } = renderHook(() => useRunDecisions(undefined), { wrapper })
    expect(result.current.fetchStatus).toBe('idle')
    expect(result.current.decisions).toEqual([])
  })

  it('surfaces an empty list rather than undefined', async () => {
    server.use(
      http.get('/api/v1/runs/run1/decisions', () => HttpResponse.json({ data: [] })),
    )

    const { wrapper } = makeWrapper()
    const { result } = renderHook(() => useRunDecisions('run1'), { wrapper })

    await waitFor(() => expect(result.current.status).toBe('success'))
    expect(result.current.decisions).toEqual([])
  })
})

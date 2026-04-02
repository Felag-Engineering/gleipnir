import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'
import React from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { useAttentionItems } from './useAttentionItems'
import type { ApiAttentionResponse } from '@/api/types'

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client }, children)
  }
}

const NOW = new Date('2026-04-02T12:00:00Z').getTime()

const APPROVAL_ITEM: ApiAttentionResponse['items'][0] = {
  type: 'approval',
  request_id: 'ar1',
  run_id: 'r1',
  policy_id: 'p1',
  policy_name: 'deploy-policy',
  tool_name: 'deploy',
  message: '',
  expires_at: new Date(NOW + 5 * 60 * 1000).toISOString(), // expires in 5 minutes
  created_at: new Date(NOW - 10 * 60 * 1000).toISOString(),
}

const FEEDBACK_ITEM: ApiAttentionResponse['items'][0] = {
  type: 'feedback',
  request_id: 'fr1',
  run_id: 'r2',
  policy_id: 'p2',
  policy_name: 'log-policy',
  tool_name: 'notify',
  message: 'Please review',
  expires_at: new Date(NOW + 30 * 60 * 1000).toISOString(), // expires in 30 minutes
  created_at: new Date(NOW - 5 * 60 * 1000).toISOString(),
}

const FAILURE_ITEM: ApiAttentionResponse['items'][0] = {
  type: 'failure',
  request_id: '',
  run_id: 'r3',
  policy_id: 'p3',
  policy_name: 'backup-policy',
  tool_name: '',
  message: 'tool call timed out',
  expires_at: null,
  created_at: new Date(NOW - 2 * 60 * 60 * 1000).toISOString(), // 2h ago
}

function setupMockAttention(items: ApiAttentionResponse['items']) {
  server.use(
    http.get('/api/v1/attention', () =>
      HttpResponse.json({ data: { items } }),
    ),
  )
}

beforeEach(() => {
  localStorage.clear()
  vi.setSystemTime(NOW)
})

describe('useAttentionItems', () => {
  it('returns empty items when no attention items exist', async () => {
    setupMockAttention([])
    const { result } = renderHook(() => useAttentionItems(), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => !result.current.isLoading)
    expect(result.current.count).toBe(0)
    expect(result.current.items).toHaveLength(0)
  })

  it('merges approval, feedback, and failure items', async () => {
    setupMockAttention([APPROVAL_ITEM, FEEDBACK_ITEM, FAILURE_ITEM])
    const { result } = renderHook(() => useAttentionItems(), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => {
      expect(result.current.items).toHaveLength(3)
    })
  })

  it('sorts by urgency: soonest expires_at first', async () => {
    // APPROVAL expires in 5 min, FEEDBACK expires in 30 min — approval should be first
    setupMockAttention([FEEDBACK_ITEM, APPROVAL_ITEM])
    const { result } = renderHook(() => useAttentionItems(), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => {
      expect(result.current.items).toHaveLength(2)
      expect(result.current.items[0].type).toBe('approval')
      expect(result.current.items[1].type).toBe('feedback')
    })
  })

  it('places failure (no expires_at) after items with closer deadlines', async () => {
    // APPROVAL expires in 5 min, FAILURE auto-expires in 22h (created 2h ago + 24h)
    setupMockAttention([FAILURE_ITEM, APPROVAL_ITEM])
    const { result } = renderHook(() => useAttentionItems(), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => {
      expect(result.current.items).toHaveLength(2)
      expect(result.current.items[0].type).toBe('approval')
      expect(result.current.items[1].type).toBe('failure')
    })
  })

  it('filters out dismissed failures from the list', async () => {
    setupMockAttention([APPROVAL_ITEM, FAILURE_ITEM])
    const { result } = renderHook(() => useAttentionItems(), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => result.current.count === 2)

    // Dismiss the failure
    act(() => {
      result.current.dismissFailure('r3')
    })

    expect(result.current.items.every(i => i.type !== 'failure')).toBe(true)
    expect(result.current.count).toBe(1)
  })

  it('persists dismissed failure IDs to localStorage', async () => {
    setupMockAttention([FAILURE_ITEM])
    const { result } = renderHook(() => useAttentionItems(), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => result.current.count === 1)

    act(() => {
      result.current.dismissFailure('r3')
    })

    const stored = JSON.parse(localStorage.getItem('gleipnir-dismissed-failures') ?? '[]') as string[]
    expect(stored).toContain('r3')
  })
})

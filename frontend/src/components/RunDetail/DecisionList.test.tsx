import { describe, it, expect } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import React from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { DecisionList } from './DecisionList'
import type { ApiRunDecision } from '@/api/types'

function renderList(decisions: ApiRunDecision[]) {
  server.use(
    http.get('/api/v1/runs/r1/decisions', () => HttpResponse.json({ data: decisions })),
  )
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    React.createElement(QueryClientProvider, { client }, React.createElement(DecisionList, { runId: 'r1' })),
  )
}

function makeDecision(overrides: Partial<ApiRunDecision> = {}): ApiRunDecision {
  return {
    run_id: 'r1',
    request_id: 'req-1',
    type: 'tool_permission_request',
    kind: 'permission',
    severity: 'info',
    tool_name: 'relay.run_operation',
    channel_entry_id: 'gleipnir.in-app',
    channel_assurance: 'authenticated',
    link_method: 'session',
    link_verified: true,
    actor_user_id: 'u-alice',
    actor_username: 'alice',
    outcome: 'answered',
    decided_at: '2026-08-05T12:00:00Z',
    ...overrides,
  }
}

describe('DecisionList', () => {
  // The whole point of the endpoint: an auditor sees who answered a
  // tool-initiated pause, what kind of ask it was, and how it ended.
  it('renders one row per decision, naming the tool, outcome, and actor', async () => {
    renderList([makeDecision()])

    expect(await screen.findByText('Decisions')).toBeInTheDocument()
    expect(screen.getByText('relay.run_operation')).toBeInTheDocument()
    expect(screen.getByText('PERMISSION')).toBeInTheDocument()
    expect(screen.getByText('Answered')).toBeInTheDocument()
    expect(screen.getByText('by alice')).toBeInTheDocument()
  })

  // A settlement nobody acted in (timeout, cancel, replay) says so plainly
  // rather than rendering a blank actor cell.
  it('shows "no responder" when nobody acted', async () => {
    renderList([
      makeDecision({
        request_id: 'req-2',
        outcome: 'cancelled',
        link_method: 'none',
        link_verified: false,
        actor_user_id: undefined,
        actor_username: undefined,
      }),
    ])

    expect(await screen.findByText('Cancelled')).toBeInTheDocument()
    expect(screen.getByText('no responder')).toBeInTheDocument()
  })

  // A replayed-after-TTL record traces back to the original human decision
  // whose answer it spent — otherwise "Replayed" alone tells an auditor
  // nothing about which prior answer settled it.
  it('shows which request a replayed decision reused', async () => {
    renderList([
      makeDecision({
        request_id: 'req-3',
        kind: 'information',
        outcome: 'replayed_after_ttl',
        link_method: 'none',
        link_verified: false,
        actor_user_id: undefined,
        actor_username: undefined,
        replay_of_request_id: 'req-1',
      }),
    ])

    expect(await screen.findByText('Replayed')).toBeInTheDocument()
    expect(screen.getByText('replay of req-1')).toBeInTheDocument()
  })

  it('renders nothing for a run with no decisions', async () => {
    let requested = false
    server.use(
      http.get('/api/v1/runs/r1/decisions', () => {
        requested = true
        return HttpResponse.json({ data: [] })
      }),
    )
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { container } = render(
      React.createElement(QueryClientProvider, { client }, React.createElement(DecisionList, { runId: 'r1' })),
    )

    await waitFor(() => expect(requested).toBe(true))
    expect(container).toBeEmptyDOMElement()
  })
})

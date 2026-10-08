import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApiRunResponder, ApiRunStep } from '@/api/types'
import { DeciderLabel } from './DeciderLabel'
import { FeedbackBlock } from './FeedbackBlock'
import { ToolBlock } from './ToolBlock'
import { parseStep } from './types'
import type { ParsedStep, ToolBlockData } from './types'

function makeRaw(overrides: Partial<ApiRunStep>): ApiRunStep {
  return {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 0,
    type: 'thought',
    content: '{}',
    token_cost: 0,
    created_at: '2026-03-10T12:00:00Z',
    ...overrides,
  }
}

function responder(overrides: Partial<ApiRunResponder>): ApiRunResponder {
  return {
    request_id: 'ar-1',
    kind: 'approval',
    status: 'approved',
    decided_at: '2026-03-10T12:01:00Z',
    decided_by: { id: 'u-alice', username: 'alice' },
    ...overrides,
  }
}

function withClient(ui: React.ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>
}

// A denied/timed-out gate: approval_request with no tool_call after it.
function approvalOnlyBlock(): ToolBlockData {
  return {
    approval: parseStep(makeRaw({
      id: 'step-approval',
      type: 'approval_request',
      content: JSON.stringify({ approval_id: 'ar-1', tool: 'srv.deploy', input: {} }),
    })) as ToolBlockData['approval'],
    call: null,
    result: null,
  }
}

function renderToolBlock(responders: ReadonlyMap<string, ApiRunResponder>) {
  return render(withClient(
    <ToolBlock block={approvalOnlyBlock()} runId="run-1" runStatus="failed" responders={responders} />,
  ))
}

describe('DeciderLabel', () => {
  it('names the verb and the user', () => {
    render(<DeciderLabel verb="Answered" username="bob" />)
    expect(screen.getByText(/Answered by/)).toBeInTheDocument()
    expect(screen.getByText('bob')).toBeInTheDocument()
  })
})

describe('ToolBlock — who decided the approval', () => {
  it('shows "Denied by" for a rejected approval', () => {
    renderToolBlock(new Map([['ar-1', responder({ status: 'rejected' })]]))
    expect(screen.getByText(/Denied by/)).toBeInTheDocument()
    expect(screen.getByText('alice')).toBeInTheDocument()
  })

  it('shows "Approved by" for an approved gate', () => {
    renderToolBlock(new Map([['ar-1', responder({ status: 'approved' })]]))
    expect(screen.getByText(/Approved by/)).toBeInTheDocument()
  })

  it.each([
    ['a timeout (system decision)', responder({ status: 'timeout', decided_by: null })],
    ['a deleted user', responder({ status: 'approved', decided_by: null })],
  ])('shows no decider for %s', (_name, r) => {
    renderToolBlock(new Map([['ar-1', r]]))
    expect(screen.queryByText(/by$/)).not.toBeInTheDocument()
    expect(screen.queryByText('alice')).not.toBeInTheDocument()
  })

  it('shows no decider when the responders do not include this approval', () => {
    renderToolBlock(new Map())
    expect(screen.queryByText(/Approved by|Denied by/)).not.toBeInTheDocument()
  })
})

describe('FeedbackBlock — who answered', () => {
  const requestStep = parseStep(makeRaw({
    id: 'step-fb',
    type: 'feedback_request',
    content: JSON.stringify({ tool: 'gleipnir.ask_operator', message: 'Which region?', feedback_id: 'fb-1' }),
  })) as ParsedStep & { type: 'feedback_request' }

  it('shows "Answered by" once a user resolved it', () => {
    const responders = new Map([['fb-1', responder({ request_id: 'fb-1', kind: 'feedback', status: 'resolved', decided_by: { id: 'u-bob', username: 'bob' } })]])
    render(withClient(<FeedbackBlock step={requestStep} runId="run-1" runStatus="complete" responders={responders} />))
    expect(screen.getByText(/Answered by/)).toBeInTheDocument()
    expect(screen.getByText('bob')).toBeInTheDocument()
  })

  it('shows nothing for a timed-out request', () => {
    const responders = new Map([['fb-1', responder({ request_id: 'fb-1', kind: 'feedback', status: 'timed_out', decided_by: null })]])
    render(withClient(<FeedbackBlock step={requestStep} runId="run-1" runStatus="failed" responders={responders} />))
    expect(screen.queryByText(/Answered by/)).not.toBeInTheDocument()
  })
})

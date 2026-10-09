// MUST-STAY-GREEN COMPAT TEST for the v1 -> v2 plugin cutover (#963): run
// history recorded by the v1 gRPC plugin runtime must keep rendering after the
// v1 code is deleted. The #1004 purge must re-run this file.
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApiRunStep } from '@/api/types'
import { GRPC_ERA_STEPS } from './__fixtures__/grpcEraRun'
import { StepTimeline } from './StepTimeline'
import { pairToolBlocks, parseStep } from './types'

function renderTimeline(raw: ApiRunStep[]) {
  const parsed = raw.map(parseStep)
  const snapshot = parsed.find((p) => p.type === 'capability_snapshot') ?? null
  const rest = parsed.filter((p) => p.type !== 'capability_snapshot')
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <StepTimeline
        items={pairToolBlocks(rest)}
        snapshot={snapshot}
        runId="run-grpc-era"
        runStatus="complete"
      />
    </QueryClientProvider>,
  )
}

describe('gRPC-era run history', () => {
  it('parses every step into a known type', () => {
    const unknown = GRPC_ERA_STEPS.map(parseStep).filter((p) => p.type === 'unknown')
    expect(unknown).toEqual([])
  })

  it('renders every step', () => {
    renderTimeline(GRPC_ERA_STEPS)

    expect(screen.getByRole('region', { name: /capability snapshot/i })).toBeInTheDocument()
    expect(screen.getAllByText('post_message').length).toBeGreaterThan(0)
    expect(screen.getAllByText('delete_channel').length).toBeGreaterThan(0)

    // One entry per rendered block: snapshot, thought, two paired tool blocks
    // (one approval-gated), the feedback request and response, two errors, complete.
    const items = screen.getByRole('list', { name: /run steps/i }).children
    expect(items).toHaveLength(9)

    expect(screen.getByText('feedback_dispatch_error')).toBeInTheDocument()
    expect(screen.getByText('plugin request timed out after 1m0s')).toBeInTheDocument()
    expect(screen.getByText('plugin_request_timeout')).toBeInTheDocument()
  })

  it('renders an unknown error kind as a generic error step', () => {
    const future: ApiRunStep = {
      ...GRPC_ERA_STEPS[9],
      id: 'stp-unknown-kind',
      content: JSON.stringify({ message: 'something new went wrong', code: 'kind_from_the_future', kind: 'kind_from_the_future' }),
    }
    renderTimeline([future])

    expect(screen.getByText('Error')).toBeInTheDocument()
    expect(screen.getByText('something new went wrong')).toBeInTheDocument()
    expect(screen.getByText('kind_from_the_future')).toBeInTheDocument()
  })
})

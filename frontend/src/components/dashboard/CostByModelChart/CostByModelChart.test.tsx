import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CostByModelChart } from './CostByModelChart'
import type { ApiTimeSeriesResponse } from '@/api/types'

function makeEmpty(): ApiTimeSeriesResponse {
  const now = Date.now()
  return {
    buckets: Array.from({ length: 24 }, (_, i) => ({
      timestamp: new Date(now - (23 - i) * 3600 * 1000).toISOString(),
      completed: 0,
      failed: 0,
      waiting_for_approval: 0,
      waiting_for_feedback: 0,
      cost_by_model: {},
    })),
  }
}

describe('CostByModelChart', () => {
  it('renders the empty-state copy for a healthy but empty account', () => {
    render(<CostByModelChart data={makeEmpty()} isLoading={false} />)

    expect(screen.getByText('No runs yet')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('renders a distinct error affordance (not the empty copy) when the fetch failed', () => {
    render(<CostByModelChart data={undefined} isLoading={false} isError />)

    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load cost data")
    expect(screen.queryByText('No runs yet')).not.toBeInTheDocument()
  })

  it('renders a Retry button that invokes onRetry when provided', async () => {
    const onRetry = vi.fn()
    render(<CostByModelChart data={undefined} isLoading={false} isError onRetry={onRetry} />)

    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalledOnce()
  })

  it('shows the skeleton while loading and neither error nor empty copy', () => {
    render(<CostByModelChart data={undefined} isLoading={true} />)

    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByText('No runs yet')).not.toBeInTheDocument()
  })
})

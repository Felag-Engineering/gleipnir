import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { RunActivityChart } from './RunActivityChart'
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

describe('RunActivityChart', () => {
  it('renders the empty-state copy for a healthy but empty account', () => {
    render(<RunActivityChart data={makeEmpty()} isLoading={false} />)

    expect(screen.getByText('No runs in the last 24h')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('renders a distinct error affordance (not the empty copy) when the fetch failed', () => {
    render(<RunActivityChart data={undefined} isLoading={false} isError />)

    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load run activity")
    expect(screen.queryByText('No runs in the last 24h')).not.toBeInTheDocument()
  })

  it('renders a Retry button that invokes onRetry when provided', async () => {
    const onRetry = vi.fn()
    render(<RunActivityChart data={undefined} isLoading={false} isError onRetry={onRetry} />)

    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalledOnce()
  })

  it('shows the skeleton while loading and neither error nor empty copy', () => {
    render(<RunActivityChart data={undefined} isLoading={true} />)

    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByText('No runs in the last 24h')).not.toBeInTheDocument()
  })
})

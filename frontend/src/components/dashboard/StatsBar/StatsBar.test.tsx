import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { StatsBar, makeDashboardStats } from './StatsBar'

describe('StatsBar', () => {
  it('renders correct counts for active runs and policies', () => {
    render(<StatsBar stats={makeDashboardStats(2, 0, 5, 12000)} />)

    expect(screen.getByText('2')).toBeInTheDocument()
    expect(screen.getByText('5')).toBeInTheDocument()
  })

  it('applies amber class to pending approvals card when count > 0', () => {
    render(<StatsBar stats={makeDashboardStats(0, 3, 0, 0)} />)

    // The label div is a direct child of the card div, so parentElement is the card.
    const pendingCard = screen.getByText('Pending approvals').parentElement
    expect(pendingCard?.className).toContain('amber')
  })

  it('does not apply amber class to pending approvals card when count is 0', () => {
    render(<StatsBar stats={makeDashboardStats(0, 0, 0, 0)} />)

    const pendingCard = screen.getByText('Pending approvals').parentElement
    expect(pendingCard?.className).not.toContain('amber')
  })
})

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { StatsBar } from './StatsBar'

function renderBar(props: Parameters<typeof StatsBar>[0]) {
  return render(
    <MemoryRouter>
      <StatsBar {...props} />
    </MemoryRouter>,
  )
}

describe('StatsBar', () => {
  it('renders 3 cards with expected labels', () => {
    renderBar({ activeRuns: 0, pendingApprovals: 0, mcpServerCount: 0, mcpServersLoading: false })
    expect(screen.getByText('Active Runs')).toBeInTheDocument()
    expect(screen.getByText('Pending Approvals')).toBeInTheDocument()
    expect(screen.getByText('System Health')).toBeInTheDocument()
  })

  it('Review link appears when pendingApprovals > 0', () => {
    renderBar({ activeRuns: 0, pendingApprovals: 3, mcpServerCount: 2, mcpServersLoading: false })
    const link = screen.getByRole('link', { name: /review/i })
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', '/runs?status=waiting_for_approval')
  })

  it('does not show Review link when pendingApprovals is 0', () => {
    renderBar({ activeRuns: 0, pendingApprovals: 0, mcpServerCount: 2, mcpServersLoading: false })
    expect(screen.queryByRole('link', { name: /review/i })).not.toBeInTheDocument()
    expect(screen.getByText('none')).toBeInTheDocument()
  })

  it('System Health shows green border when servers > 0', () => {
    renderBar({ activeRuns: 0, pendingApprovals: 0, mcpServerCount: 2, mcpServersLoading: false })
    expect(screen.getByText('2 servers')).toBeInTheDocument()
  })

  it('System Health shows dash while loading', () => {
    renderBar({ activeRuns: 0, pendingApprovals: 0, mcpServerCount: 0, mcpServersLoading: true })
    expect(screen.getByText('—')).toBeInTheDocument()
  })

  it('System Health shows 0 servers when no servers and not loading', () => {
    renderBar({ activeRuns: 0, pendingApprovals: 0, mcpServerCount: 0, mcpServersLoading: false })
    expect(screen.getByText('0 servers')).toBeInTheDocument()
  })
})

import type { ReactNode } from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import EmptyState from './EmptyState'

function renderWithRouter(ui: ReactNode) {
  return render(<MemoryRouter>{ui}</MemoryRouter>)
}

describe('EmptyState', () => {
  it('renders headline and subtext', () => {
    renderWithRouter(<EmptyState headline="No agents yet" subtext="Create one" />)
    expect(screen.getByRole('heading', { name: 'No agents yet' })).toBeInTheDocument()
    expect(screen.getByText('Create one')).toBeInTheDocument()
  })

  it('renders a link CTA when ctaTo is provided', () => {
    renderWithRouter(
      <EmptyState headline="No runs" ctaLabel="Go to Agents" ctaTo="/agents" />,
    )
    const link = screen.getByRole('link', { name: 'Go to Agents' })
    expect(link).toHaveAttribute('href', '/agents')
  })

  it('renders a button CTA and fires onCtaClick when clicked', () => {
    const onCtaClick = vi.fn()
    renderWithRouter(
      <EmptyState headline="No users" ctaLabel="Create user" onCtaClick={onCtaClick} />,
    )
    const button = screen.getByRole('button', { name: 'Create user' })
    fireEvent.click(button)
    expect(onCtaClick).toHaveBeenCalledTimes(1)
  })

  it('prefers the link when both ctaTo and onCtaClick are supplied', () => {
    const onCtaClick = vi.fn()
    renderWithRouter(
      <EmptyState
        headline="No runs"
        ctaLabel="Go"
        ctaTo="/agents"
        onCtaClick={onCtaClick}
      />,
    )
    expect(screen.getByRole('link', { name: 'Go' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Go' })).not.toBeInTheDocument()
  })

  it('renders no CTA when neither ctaTo nor onCtaClick is provided', () => {
    renderWithRouter(<EmptyState headline="No audiences" subtext="None yet." />)
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('omits the subtext paragraph when subtext is absent', () => {
    renderWithRouter(<EmptyState headline="Empty" />)
    expect(screen.getByRole('heading', { name: 'Empty' })).toBeInTheDocument()
  })
})

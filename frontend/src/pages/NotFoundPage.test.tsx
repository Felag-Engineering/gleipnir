import React from 'react'
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { NotFoundPage } from './NotFoundPage'

function renderNotFoundPage(props?: React.ComponentProps<typeof NotFoundPage>) {
  return render(
    <MemoryRouter>
      <NotFoundPage {...props} />
    </MemoryRouter>,
  )
}

describe('NotFoundPage', () => {
  it('renders 404 error code', () => {
    renderNotFoundPage()
    expect(screen.getByText('404')).toBeInTheDocument()
  })

  it('renders page not found heading', () => {
    renderNotFoundPage()
    expect(screen.getByRole('heading', { name: /page not found/i })).toBeInTheDocument()
  })

  it('renders a descriptive message', () => {
    renderNotFoundPage()
    expect(screen.getByText(/does not exist or has been moved/i)).toBeInTheDocument()
  })

  it('renders a link to the dashboard', () => {
    renderNotFoundPage()
    const link = screen.getByRole('link', { name: /go to dashboard/i })
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', '/dashboard')
  })
})

describe('NotFoundPage — custom props', () => {
  it('renders custom title as heading', () => {
    renderNotFoundPage({ title: 'Run not found' })
    expect(screen.getByRole('heading', { name: /run not found/i })).toBeInTheDocument()
  })

  it('renders custom message text', () => {
    renderNotFoundPage({ message: 'No run with that ID.' })
    expect(screen.getByText('No run with that ID.')).toBeInTheDocument()
  })

  it('renders custom primary CTA link with correct href', () => {
    renderNotFoundPage({ primary: { label: 'Go to Run History', to: '/runs' } })
    const link = screen.getByRole('link', { name: /go to run history/i })
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', '/runs')
  })

  it('renders secondary CTA link with correct href when provided', () => {
    renderNotFoundPage({
      primary: { label: 'Go to Run History', to: '/runs' },
      secondary: { label: 'Go to Dashboard', to: '/dashboard' },
    })
    const link = screen.getByRole('link', { name: /go to dashboard/i })
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', '/dashboard')
  })

  it('does not render secondary link when not provided', () => {
    renderNotFoundPage({ primary: { label: 'Go to Run History', to: '/runs' } })
    // Only one link rendered — no secondary
    const links = screen.getAllByRole('link')
    expect(links).toHaveLength(1)
  })
})

describe('NotFoundPage — embedded mode', () => {
  it('renders the container wrapper when embedded is false (default)', () => {
    renderNotFoundPage()
    expect(screen.getByTestId('not-found-container')).toBeInTheDocument()
  })

  it('does not render the container wrapper when embedded is true', () => {
    renderNotFoundPage({ embedded: true })
    expect(screen.queryByTestId('not-found-container')).not.toBeInTheDocument()
  })

  it('still renders the 404 code and heading in embedded mode', () => {
    renderNotFoundPage({ embedded: true, title: 'Agent not found' })
    expect(screen.getByText('404')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: /agent not found/i })).toBeInTheDocument()
  })
})

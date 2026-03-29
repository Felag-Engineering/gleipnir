import React from 'react'
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import NotFoundPage from './NotFoundPage'

function renderNotFoundPage() {
  return render(
    <MemoryRouter>
      <NotFoundPage />
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

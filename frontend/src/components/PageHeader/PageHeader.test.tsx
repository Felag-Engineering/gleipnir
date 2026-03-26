import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { PageHeader } from './PageHeader'

describe('PageHeader', () => {
  it('renders the title text in an h1', () => {
    render(<PageHeader title="Dashboard" />)
    const heading = screen.getByRole('heading', { level: 1 })
    expect(heading).toHaveTextContent('Dashboard')
  })

  it('renders children in the actions area', () => {
    render(
      <PageHeader title="Policies">
        <button>New Policy</button>
      </PageHeader>,
    )
    expect(screen.getByRole('button', { name: 'New Policy' })).toBeInTheDocument()
  })

  it('renders without an actions wrapper when no children are provided', () => {
    const { container } = render(<PageHeader title="Runs" />)
    // Only the header div and h1 should be present — no extra div for actions
    const headerDiv = container.firstChild as HTMLElement
    expect(headerDiv.children).toHaveLength(1)
    expect(headerDiv.children[0].tagName).toBe('H1')
  })
})

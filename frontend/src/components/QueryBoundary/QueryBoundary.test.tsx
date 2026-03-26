import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import QueryBoundary from './QueryBoundary'

describe('QueryBoundary', () => {
  it('renders default skeleton (5 aria-hidden blocks) when status is pending', () => {
    const { container } = render(
      <QueryBoundary status="pending">
        <span>Content</span>
      </QueryBoundary>,
    )
    const blocks = container.querySelectorAll('[aria-hidden="true"]')
    expect(blocks.length).toBe(5)
    expect(screen.queryByText('Content')).not.toBeInTheDocument()
  })

  it('renders custom skeleton node when status is pending and skeleton prop is provided', () => {
    render(
      <QueryBoundary status="pending" skeleton={<div data-testid="custom-skel" />}>
        <span>Content</span>
      </QueryBoundary>,
    )
    expect(screen.getByTestId('custom-skel')).toBeInTheDocument()
    expect(screen.queryByText('Content')).not.toBeInTheDocument()
  })

  it('renders error message and Retry button when status is error and onRetry is provided', () => {
    const onRetry = vi.fn()
    render(
      <QueryBoundary status="error" onRetry={onRetry}>
        <span>Content</span>
      </QueryBoundary>,
    )
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText('Something went wrong.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByText('Content')).not.toBeInTheDocument()
  })

  it('renders error message without Retry button when status is error and onRetry is omitted', () => {
    render(
      <QueryBoundary status="error">
        <span>Content</span>
      </QueryBoundary>,
    )
    expect(screen.getByText('Something went wrong.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
  })

  it('renders custom errorMessage when provided', () => {
    render(
      <QueryBoundary status="error" errorMessage="Failed to load items.">
        <span>Content</span>
      </QueryBoundary>,
    )
    expect(screen.getByText('Failed to load items.')).toBeInTheDocument()
  })

  it('renders emptyState prop when status is success and isEmpty is true', () => {
    render(
      <QueryBoundary status="success" isEmpty emptyState={<p>Nothing here</p>}>
        <span>Content</span>
      </QueryBoundary>,
    )
    expect(screen.getByText('Nothing here')).toBeInTheDocument()
    expect(screen.queryByText('Content')).not.toBeInTheDocument()
  })

  it('renders nothing when status is success, isEmpty is true, and emptyState is not provided', () => {
    const { container } = render(
      <QueryBoundary status="success" isEmpty>
        <span>Content</span>
      </QueryBoundary>,
    )
    expect(container.firstChild).toBeNull()
    expect(screen.queryByText('Content')).not.toBeInTheDocument()
  })

  it('renders children when status is success and isEmpty is false', () => {
    render(
      <QueryBoundary status="success">
        <span>Content</span>
      </QueryBoundary>,
    )
    expect(screen.getByText('Content')).toBeInTheDocument()
  })
})

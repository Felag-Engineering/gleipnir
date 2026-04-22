import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ErrorBanner } from './ErrorBanner'

describe('ErrorBanner', () => {
  it('returns null when issues is empty', () => {
    const { container } = render(<ErrorBanner issues={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders a bulleted list when issues are provided', () => {
    render(
      <ErrorBanner
        issues={[
          { field: 'name', message: 'Name is required' },
          { field: 'agent.task', message: 'Task is required' },
        ]}
      />,
    )
    expect(screen.getByText('Name is required')).toBeInTheDocument()
    expect(screen.getByText('Task is required')).toBeInTheDocument()
  })

  it('renders issue with field as a clickable button', () => {
    const onClick = vi.fn()
    render(
      <ErrorBanner
        issues={[{ field: 'name', message: 'Name is required' }]}
        onIssueClick={onClick}
      />,
    )
    const btn = screen.getByRole('button', { name: 'Name is required' })
    fireEvent.click(btn)
    expect(onClick).toHaveBeenCalledWith('name')
  })

  it('renders issue without field as plain text (no button)', () => {
    const onClick = vi.fn()
    render(
      <ErrorBanner
        issues={[{ message: 'Server error' }]}
        onIssueClick={onClick}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Server error' })).toBeNull()
    expect(screen.getByText('Server error')).toBeInTheDocument()
  })

  it('calls onDismiss when dismiss button is clicked', () => {
    const onDismiss = vi.fn()
    render(
      <ErrorBanner
        issues={[{ message: 'Error' }]}
        onDismiss={onDismiss}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(onDismiss).toHaveBeenCalledTimes(1)
  })

  it('does not render a dismiss button when onDismiss is not provided', () => {
    render(<ErrorBanner issues={[{ message: 'Error' }]} />)
    expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull()
  })

  it('renders multiple issues with the same field as separate bullets', () => {
    render(
      <ErrorBanner
        issues={[
          { field: 'capabilities.tools[0].tool', message: 'Bad dot notation' },
          { field: 'capabilities.tools[0].tool', message: 'Duplicate tool' },
        ]}
      />,
    )
    expect(screen.getByText('Bad dot notation')).toBeInTheDocument()
    expect(screen.getByText('Duplicate tool')).toBeInTheDocument()
  })

  it('renders with role=alert', () => {
    render(<ErrorBanner issues={[{ message: 'Error' }]} />)
    expect(screen.getByRole('alert')).toBeInTheDocument()
  })

  it('uses the custom title when provided', () => {
    render(
      <ErrorBanner
        title="Please fix these issues"
        issues={[{ message: 'Error' }]}
      />,
    )
    expect(screen.getByText('Please fix these issues')).toBeInTheDocument()
  })
})

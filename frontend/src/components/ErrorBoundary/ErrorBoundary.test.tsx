import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ErrorBoundary from './ErrorBoundary'

function ThrowOnRender(): never {
  throw new Error('test render error')
}

let shouldThrow = true

function ToggleChild() {
  if (shouldThrow) {
    throw new Error('test render error')
  }
  return <div>recovered content</div>
}

describe('ErrorBoundary', () => {
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    shouldThrow = true
  })

  it('root boundary catches a render error', () => {
    render(
      <ErrorBoundary>
        <ThrowOnRender />
      </ErrorBoundary>,
    )

    expect(screen.getByText(/something went wrong/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /try again/i })).toBeInTheDocument()
    expect(screen.getByText('test render error')).toBeInTheDocument()
  })

  it('retry button resets the boundary and children render normally', () => {
    render(
      <ErrorBoundary>
        <ToggleChild />
      </ErrorBoundary>,
    )

    expect(screen.getByText(/something went wrong/i)).toBeInTheDocument()

    shouldThrow = false
    fireEvent.click(screen.getByRole('button', { name: /try again/i }))

    expect(screen.getByText('recovered content')).toBeInTheDocument()
    expect(screen.queryByText(/something went wrong/i)).toBeNull()
    expect(screen.queryByRole('button', { name: /try again/i })).toBeNull()
  })

  it('sibling boundaries are isolated', () => {
    render(
      <div>
        <ErrorBoundary>
          <ThrowOnRender />
        </ErrorBoundary>
        <ErrorBoundary>
          <div>sibling content</div>
        </ErrorBoundary>
      </div>,
    )

    expect(screen.getByText(/something went wrong/i)).toBeInTheDocument()
    expect(screen.getByText('sibling content')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /try again/i })).toHaveLength(1)
  })
})

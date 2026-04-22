import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { FieldError } from './FieldError'

describe('FieldError', () => {
  it('renders nothing when messages is empty array', () => {
    const { container } = render(<FieldError messages={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders nothing when messages is undefined', () => {
    const { container } = render(<FieldError />)
    expect(container.firstChild).toBeNull()
  })

  it('renders nothing when messages is an empty string', () => {
    const { container } = render(<FieldError messages="" />)
    expect(container.firstChild).toBeNull()
  })

  it('renders role=alert for a single message string', () => {
    render(<FieldError messages="Name is required" />)
    const el = screen.getByRole('alert')
    expect(el).toBeInTheDocument()
    expect(el).toHaveTextContent('Name is required')
  })

  it('renders role=alert for a single-element array', () => {
    render(<FieldError messages={['Name is required']} />)
    const el = screen.getByRole('alert')
    expect(el).toBeInTheDocument()
    expect(el).toHaveTextContent('Name is required')
  })

  it('renders multiple messages as a list with role=alert', () => {
    render(<FieldError messages={['Error one', 'Error two']} />)
    const el = screen.getByRole('alert')
    expect(el.tagName).toBe('UL')
    expect(screen.getByText('Error one')).toBeInTheDocument()
    expect(screen.getByText('Error two')).toBeInTheDocument()
  })

  it('forwards the id prop to the root element', () => {
    render(<FieldError id="my-error" messages="oops" />)
    expect(screen.getByRole('alert')).toHaveAttribute('id', 'my-error')
  })

  it('filters out empty strings from the messages array', () => {
    const { container } = render(<FieldError messages={['', '']} />)
    expect(container.firstChild).toBeNull()
  })
})

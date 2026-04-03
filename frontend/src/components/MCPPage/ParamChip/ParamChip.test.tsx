import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ParamChip } from './ParamChip'

describe('ParamChip', () => {
  it('renders parameter name and type', () => {
    render(<ParamChip name="message" type="string" required={false} />)
    expect(screen.getByText('message')).toBeInTheDocument()
    expect(screen.getByText('string')).toBeInTheDocument()
  })

  it('shows required badge when required is true', () => {
    render(<ParamChip name="channel" type="string" required={true} />)
    expect(screen.getByText('required')).toBeInTheDocument()
  })

  it('hides required badge when required is false', () => {
    render(<ParamChip name="limit" type="integer" required={false} />)
    expect(screen.queryByText('required')).not.toBeInTheDocument()
  })
})

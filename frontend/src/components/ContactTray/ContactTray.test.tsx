import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ContactTray } from './ContactTray'

describe('ContactTray — visibility', () => {
  it('renders nothing when closed', () => {
    const { container } = render(<ContactTray open={false} onClose={vi.fn()} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('renders the dialog with heading when open', () => {
    render(<ContactTray open onClose={vi.fn()} />)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('Get in touch')).toBeInTheDocument()
  })
})

describe('ContactTray — contact links', () => {
  it('renders the email as a mailto link', () => {
    render(<ContactTray open onClose={vi.fn()} />)
    const email = screen.getByRole('link', { name: /email/i })
    expect(email).toHaveAttribute('href', 'mailto:support@gleipnir.dev')
  })

  it('renders the GitHub link opening in a new tab with safe rel', () => {
    render(<ContactTray open onClose={vi.fn()} />)
    const gh = screen.getByRole('link', { name: /github/i })
    expect(gh).toHaveAttribute('href', 'https://github.com/Felag-Engineering/gleipnir')
    expect(gh).toHaveAttribute('target', '_blank')
    expect(gh).toHaveAttribute('rel', 'noopener noreferrer')
  })
})

describe('ContactTray — close interactions', () => {
  it('calls onClose when the close button is clicked', () => {
    const onClose = vi.fn()
    render(<ContactTray open onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('calls onClose when Escape is pressed', () => {
    const onClose = vi.fn()
    render(<ContactTray open onClose={onClose} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

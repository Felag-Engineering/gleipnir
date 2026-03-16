import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import React from 'react'
import { Modal } from './Modal'

describe('Modal — rendering', () => {
  it('renders title', () => {
    render(<Modal title="Test Modal" onClose={vi.fn()}><p>Content</p></Modal>)
    expect(screen.getByText('Test Modal')).toBeInTheDocument()
  })

  it('renders children', () => {
    render(<Modal title="T" onClose={vi.fn()}><p>Child content</p></Modal>)
    expect(screen.getByText('Child content')).toBeInTheDocument()
  })

  it('renders footer when provided', () => {
    render(
      <Modal title="T" onClose={vi.fn()} footer={<button>Confirm</button>}>
        <p>Body</p>
      </Modal>,
    )
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeInTheDocument()
  })

  it('does not render footer element when omitted', () => {
    const { container } = render(<Modal title="T" onClose={vi.fn()}><p>Body</p></Modal>)
    // The footer div is only rendered when footer prop is provided
    // Check there's no extra div wrapping beyond what's needed
    // We verify by checking that no footer content exists
    expect(screen.queryByText('Confirm')).not.toBeInTheDocument()
    // The footer conditional renders nothing — only 1 role=dialog present
    expect(container.querySelector('[class*="footer"]')).not.toBeInTheDocument()
  })
})

describe('Modal — close interactions', () => {
  it('calls onClose when close button is clicked', () => {
    const onClose = vi.fn()
    render(<Modal title="T" onClose={onClose}><p>Body</p></Modal>)
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('calls onClose when Escape key is pressed', () => {
    const onClose = vi.fn()
    render(<Modal title="T" onClose={onClose}><p>Body</p></Modal>)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('calls onClose when overlay (dialog element) is clicked directly', () => {
    const onClose = vi.fn()
    render(<Modal title="T" onClose={onClose}><p>Body</p></Modal>)
    const overlay = screen.getByRole('dialog')
    // Simulate a click where currentTarget === target (direct overlay click)
    fireEvent.click(overlay)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('does not call onClose when clicking inside the modal box', () => {
    const onClose = vi.fn()
    render(<Modal title="T" onClose={onClose}><p>Inner content</p></Modal>)
    // Click on the inner content — this should NOT bubble to close the modal
    fireEvent.click(screen.getByText('Inner content'))
    expect(onClose).not.toHaveBeenCalled()
  })
})

describe('Modal — accessibility', () => {
  it('has role="dialog" and aria-modal="true"', () => {
    render(<Modal title="T" onClose={vi.fn()}><p>Body</p></Modal>)
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
  })

  it('title element has id="modal-title"', () => {
    render(<Modal title="Accessible Title" onClose={vi.fn()}><p>Body</p></Modal>)
    const title = screen.getByText('Accessible Title')
    expect(title).toHaveAttribute('id', 'modal-title')
  })
})

describe('Modal — cleanup', () => {
  it('does not call onClose after unmount when Escape is pressed', () => {
    const onClose = vi.fn()
    const { unmount } = render(<Modal title="T" onClose={onClose}><p>Body</p></Modal>)
    unmount()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
  })
})

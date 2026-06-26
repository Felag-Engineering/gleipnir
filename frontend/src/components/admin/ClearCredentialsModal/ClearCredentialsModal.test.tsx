import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ClearCredentialsModal } from './ClearCredentialsModal'

function renderModal(
  overrides: Partial<React.ComponentProps<typeof ClearCredentialsModal>> = {},
) {
  const defaults: React.ComponentProps<typeof ClearCredentialsModal> = {
    onClose: vi.fn(),
    onConfirm: vi.fn(),
    isPending: false,
    error: null,
  }
  return render(<ClearCredentialsModal {...defaults} {...overrides} />)
}

describe('ClearCredentialsModal', () => {
  it('states the re-authorization consequence', () => {
    renderModal()
    expect(screen.getByText(/you'll need to re-authorize/i)).toBeInTheDocument()
  })

  it('calls onConfirm when Clear credentials button is clicked', async () => {
    const onConfirm = vi.fn()
    renderModal({ onConfirm })
    await userEvent.click(screen.getByRole('button', { name: /clear credentials/i }))
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it('calls onClose when Cancel is clicked', async () => {
    const onClose = vi.fn()
    renderModal({ onClose })
    await userEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('disables the confirm button while pending', () => {
    renderModal({ isPending: true })
    expect(screen.getByRole('button', { name: /clear/i })).toBeDisabled()
  })

  it('shows error message under role=alert when error is set', () => {
    renderModal({ error: 'Clear failed.' })
    expect(screen.getByRole('alert')).toHaveTextContent('Clear failed.')
  })

  it('does not render alert element when error is null', () => {
    renderModal({ error: null })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

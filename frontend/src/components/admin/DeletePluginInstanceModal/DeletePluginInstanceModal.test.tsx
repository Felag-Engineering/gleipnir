import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DeletePluginInstanceModal } from './DeletePluginInstanceModal'

function renderModal(overrides: Partial<React.ComponentProps<typeof DeletePluginInstanceModal>> = {}) {
  const defaults: React.ComponentProps<typeof DeletePluginInstanceModal> = {
    pluginName: 'my-slack-plugin',
    instanceName: 'prod',
    onClose: vi.fn(),
    onConfirm: vi.fn(),
    isPending: false,
    error: null,
  }
  return render(<DeletePluginInstanceModal {...defaults} {...overrides} />)
}

describe('DeletePluginInstanceModal', () => {
  it('renders plugin and instance names', () => {
    renderModal()
    expect(screen.getByText('my-slack-plugin')).toBeInTheDocument()
    expect(screen.getByText('prod')).toBeInTheDocument()
  })

  it('calls onConfirm when Delete instance button is clicked', async () => {
    const onConfirm = vi.fn()
    renderModal({ onConfirm })
    await userEvent.click(screen.getByRole('button', { name: /delete instance/i }))
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it('calls onClose when Cancel is clicked', async () => {
    const onClose = vi.fn()
    renderModal({ onClose })
    await userEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('disables the delete button while pending', () => {
    renderModal({ isPending: true })
    expect(screen.getByRole('button', { name: /delet/i })).toBeDisabled()
  })

  it('shows error message under role=alert when error is set', () => {
    renderModal({ error: 'Policy "Nightly Sync" still references this instance.' })
    expect(screen.getByRole('alert')).toHaveTextContent('Policy "Nightly Sync"')
  })

  it('does not render alert element when error is null', () => {
    renderModal({ error: null })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

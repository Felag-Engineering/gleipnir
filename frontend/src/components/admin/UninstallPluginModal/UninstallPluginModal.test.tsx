import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { UninstallPluginModal } from './UninstallPluginModal'

function renderModal(overrides: Partial<React.ComponentProps<typeof UninstallPluginModal>> = {}) {
  const defaults: React.ComponentProps<typeof UninstallPluginModal> = {
    pluginName: 'my-slack-plugin',
    instanceNames: ['prod', 'staging'],
    onClose: vi.fn(),
    onConfirm: vi.fn(),
    isPending: false,
    error: null,
  }
  return render(<UninstallPluginModal {...defaults} {...overrides} />)
}

describe('UninstallPluginModal', () => {
  it('renders the plugin name', () => {
    renderModal()
    expect(screen.getByText('my-slack-plugin')).toBeInTheDocument()
  })

  it('lists all instance names', () => {
    renderModal()
    expect(screen.getByText('prod')).toBeInTheDocument()
    expect(screen.getByText('staging')).toBeInTheDocument()
  })

  it('does not render instance list when instanceNames is empty', () => {
    renderModal({ instanceNames: [] })
    expect(screen.queryByText(/instances that will be removed/i)).not.toBeInTheDocument()
  })

  it('calls onConfirm when Uninstall plugin button is clicked', async () => {
    const onConfirm = vi.fn()
    renderModal({ onConfirm })
    await userEvent.click(screen.getByRole('button', { name: /uninstall plugin/i }))
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
    expect(screen.getByRole('button', { name: /uninstall/i })).toBeDisabled()
  })

  it('shows error message under role=alert when error is set', () => {
    renderModal({ error: 'Audience "ops-team" references this plugin.' })
    expect(screen.getByRole('alert')).toHaveTextContent('Audience "ops-team"')
  })

  it('does not render alert when error is null', () => {
    renderModal()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

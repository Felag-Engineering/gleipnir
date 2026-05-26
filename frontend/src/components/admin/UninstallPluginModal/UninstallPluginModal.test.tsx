import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { UninstallPluginModal } from './UninstallPluginModal'

function renderModal(overrides: Partial<React.ComponentProps<typeof UninstallPluginModal>> = {}) {
  const defaults: React.ComponentProps<typeof UninstallPluginModal> = {
    pluginName: 'my-slack-plugin',
    // Default to empty — this is the normal uninstall-ready state.
    instanceNames: [],
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

  it('calls onConfirm when Uninstall plugin button is clicked (zero instances)', async () => {
    const onConfirm = vi.fn()
    renderModal({ onConfirm, instanceNames: [] })
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
    renderModal({ isPending: true, instanceNames: [] })
    expect(screen.getByRole('button', { name: /uninstall/i })).toBeDisabled()
  })

  it('shows error message under role=alert when error is set', () => {
    renderModal({ error: 'Audience "ops-team" references this plugin.', instanceNames: [] })
    expect(screen.getByRole('alert')).toHaveTextContent('Audience "ops-team"')
  })

  it('does not render alert when error is null', () => {
    renderModal()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  // ── Blocked state (instances still exist) ─────────────────────────────────

  it('shows "Cannot uninstall" and disables submit when instances remain', () => {
    renderModal({ instanceNames: ['prod', 'staging'] })
    const btn = screen.getByRole('button', { name: /cannot uninstall/i })
    expect(btn).toBeDisabled()
  })

  it('lists remaining instance names when blocked', () => {
    renderModal({ instanceNames: ['prod', 'staging'] })
    expect(screen.getByText('prod')).toBeInTheDocument()
    expect(screen.getByText('staging')).toBeInTheDocument()
  })

  it('shows "Remaining instances" label when blocked', () => {
    renderModal({ instanceNames: ['prod'] })
    expect(screen.getByText(/remaining instances/i)).toBeInTheDocument()
  })

  it('does not show "Remaining instances:" label when no instances remain', () => {
    renderModal({ instanceNames: [] })
    // The zero-state message uses lowercase "remaining instances" in a sentence.
    // Match the section header text specifically.
    expect(screen.queryByText('Remaining instances:')).not.toBeInTheDocument()
  })
})

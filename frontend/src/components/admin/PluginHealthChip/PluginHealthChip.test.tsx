import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { PluginHealthChip } from './PluginHealthChip'
import type { PluginHealthState } from '@/api/types'

describe('PluginHealthChip', () => {
  it('renders the label for each state', () => {
    const cases: Array<[PluginHealthState, string]> = [
      ['healthy', 'Healthy'],
      ['unsigned_permissive', 'Unsigned (permissive)'],
      ['pending_key_approval', 'Pending key approval'],
      ['pending_manifest_approval', 'Pending manifest approval'],
      ['pending_config_migration', 'Pending config migration'],
      ['unhealthy', 'Unhealthy'],
      ['circuit_broken', 'Circuit broken'],
      ['verification_error', 'Verification error'],
      ['signature_invalid', 'Signature invalid'],
      ['crashed', 'Crashed'],
    ]
    for (const [state, label] of cases) {
      const { unmount } = render(<PluginHealthChip state={state} />)
      expect(screen.getByText(label)).toBeInTheDocument()
      unmount()
    }
  })

  it('calls onClick when clicked', async () => {
    const handler = vi.fn()
    render(<PluginHealthChip state="healthy" onClick={handler} />)
    await userEvent.click(screen.getByRole('button', { name: 'Healthy' }))
    expect(handler).toHaveBeenCalledOnce()
  })

  it('sets the title attribute to the detail prop', () => {
    const detail = 'Manifest hash mismatch'
    render(<PluginHealthChip state="verification_error" detail={detail} />)
    expect(screen.getByRole('button')).toHaveAttribute('title', detail)
  })

  it('renders a green variant for healthy state', () => {
    render(<PluginHealthChip state="healthy" />)
    const btn = screen.getByRole('button')
    // CSS Modules generate a hashed class name; we verify a class is present.
    // The exact class name is verified by the green/yellow/red naming convention
    // in the CSS module — this test confirms the class is applied at all.
    expect(btn.className).toMatch(/green/)
  })

  it('renders a yellow variant for pending states', () => {
    render(<PluginHealthChip state="pending_key_approval" />)
    expect(screen.getByRole('button').className).toMatch(/yellow/)
  })

  it('renders a red variant for error states', () => {
    render(<PluginHealthChip state="crashed" />)
    expect(screen.getByRole('button').className).toMatch(/red/)
  })
})

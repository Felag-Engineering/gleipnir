import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { SaveGuardDialog } from './SaveGuardDialog'
import type { ApiAudienceReferences } from '@/api/types'

function renderDialog(refs: ApiAudienceReferences, props?: Partial<Parameters<typeof SaveGuardDialog>[0]>) {
  return render(
    <MemoryRouter>
      <SaveGuardDialog
        references={refs}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
        isPending={false}
        {...props}
      />
    </MemoryRouter>,
  )
}

const twoRefs: ApiAudienceReferences = {
  policies: [
    { id: 'p1', name: 'deploy-bot' },
    { id: 'p2', name: 'smoke-tests' },
  ],
  in_flight_runs: [],
}

const refsWithInFlight: ApiAudienceReferences = {
  policies: [{ id: 'p1', name: 'deploy-bot' }],
  in_flight_runs: [
    { id: 'run-abc123', policy_id: 'p1', status: 'running' },
    { id: 'run-def456', policy_id: 'p1', status: 'waiting_for_approval' },
  ],
}

describe('SaveGuardDialog', () => {
  it('renders policy count and policy names', () => {
    renderDialog(twoRefs)
    // The count is in a <strong> element; check container textContent.
    const dialog = screen.getByRole('dialog')
    expect(dialog.textContent).toMatch(/2.*polic/i)
    expect(screen.getByRole('link', { name: 'deploy-bot' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'smoke-tests' })).toBeInTheDocument()
  })

  it('does not show in-flight warning when no in-flight runs', () => {
    renderDialog(twoRefs)
    expect(screen.queryByText(/in-flight/i)).not.toBeInTheDocument()
  })

  it('shows in-flight count and warning text when in_flight_runs is non-empty', () => {
    renderDialog(refsWithInFlight)
    // The count (2) is in a <strong> element; the surrounding text contains "in-flight".
    // Use the alert role to find the whole warning container.
    const warning = screen.getByRole('alert')
    expect(warning.textContent).toMatch(/2.*in-flight/i)
    expect(warning.textContent).toMatch(/change applies to subsequent steps only/i)
  })

  it('calls onCancel when Cancel is clicked', async () => {
    const onCancel = vi.fn()
    renderDialog(twoRefs, { onCancel })
    await userEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onCancel).toHaveBeenCalledOnce()
  })

  it('calls onConfirm when Save anyway is clicked', async () => {
    const onConfirm = vi.fn()
    renderDialog(twoRefs, { onConfirm })
    await userEvent.click(screen.getByRole('button', { name: /save anyway/i }))
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it('disables Save anyway and shows spinner when isPending', () => {
    renderDialog(twoRefs, { isPending: true })
    const btn = screen.getByRole('button', { name: /saving/i })
    expect(btn).toBeDisabled()
  })

  it('policy links point to the correct /agents/:id route', () => {
    renderDialog(twoRefs)
    const link = screen.getByRole('link', { name: 'deploy-bot' })
    expect(link).toHaveAttribute('href', '/agents/p1')
  })
})

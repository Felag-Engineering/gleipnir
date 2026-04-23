import { describe, it, expect, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { SetupChecklist } from './SetupChecklist'

function renderChecklist(props: {
  hasModel?: boolean
  hasServer?: boolean
  hasAgent?: boolean
  hasFirstRun?: boolean
  isLoading?: boolean
}) {
  return render(
    <MemoryRouter>
      <SetupChecklist
        hasModel={props.hasModel ?? false}
        hasServer={props.hasServer ?? false}
        hasAgent={props.hasAgent ?? false}
        hasFirstRun={props.hasFirstRun ?? false}
        isLoading={props.isLoading ?? false}
      />
    </MemoryRouter>,
  )
}

describe('SetupChecklist', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('renders all four steps when nothing is done', () => {
    renderChecklist({})

    expect(screen.getByText('Add a model API key')).toBeInTheDocument()
    expect(screen.getByText('Register an MCP server')).toBeInTheDocument()
    expect(screen.getByText('Create an agent')).toBeInTheDocument()
    expect(screen.getByText('Trigger your first run')).toBeInTheDocument()
  })

  it('shows pending icons for incomplete steps and done icons for completed ones', () => {
    renderChecklist({ hasModel: true })

    const doneIcons = screen.getAllByLabelText('done')
    const pendingIcons = screen.getAllByLabelText('pending')

    expect(doneIcons).toHaveLength(1)
    expect(pendingIcons).toHaveLength(3)
  })

  it('shows CTA links pointing to the correct routes', () => {
    renderChecklist({})

    expect(screen.getByRole('link', { name: 'Go to Models' })).toHaveAttribute('href', '/admin/models')
    expect(screen.getByRole('link', { name: 'Go to Tools' })).toHaveAttribute('href', '/tools')
    expect(screen.getByRole('link', { name: 'New Agent' })).toHaveAttribute('href', '/agents/new')
    expect(screen.getByRole('link', { name: 'Go to Agents' })).toHaveAttribute('href', '/agents')
  })

  it('does not show a CTA for completed steps', () => {
    renderChecklist({ hasModel: true, hasServer: true })

    expect(screen.queryByRole('link', { name: 'Go to Models' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Go to Tools' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'New Agent' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to Agents' })).toBeInTheDocument()
  })

  it('returns null when every step is done and not loading', () => {
    const { container } = renderChecklist({
      hasModel: true,
      hasServer: true,
      hasAgent: true,
      hasFirstRun: true,
      isLoading: false,
    })

    expect(container.firstChild).toBeNull()
  })

  it('does not return null when loading even if all steps would be done', () => {
    renderChecklist({
      hasModel: true,
      hasServer: true,
      hasAgent: true,
      hasFirstRun: true,
      isLoading: true,
    })

    expect(screen.getByText('SETUP')).toBeInTheDocument()
  })

  it('collapses and expands on toggle, persisting to localStorage', async () => {
    renderChecklist({})

    expect(screen.getByText('Add a model API key')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button'))

    expect(screen.queryByText('Add a model API key')).not.toBeInTheDocument()

    const stored = localStorage.getItem('gleipnir-setup-collapsed')
    expect(JSON.parse(stored!)).toBe(true)

    await userEvent.click(screen.getByRole('button'))
    expect(screen.getByText('Add a model API key')).toBeInTheDocument()
  })
})

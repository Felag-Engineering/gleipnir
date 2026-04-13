import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { DeleteAgentModal } from './DeleteAgentModal'

vi.mock('@/hooks/queries/runs')

import { useRuns } from '@/hooks/queries/runs'

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderModal(props: Partial<Parameters<typeof DeleteAgentModal>[0]> = {}) {
  const defaults = {
    policyId: 'policy-123',
    policyName: 'my-test-agent',
    onClose: vi.fn(),
    onConfirm: vi.fn(),
    isPending: false,
    error: null,
  }
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <DeleteAgentModal {...defaults} {...props} />
    </QueryClientProvider>,
  )
}

function mockRuns(total: number, status: 'success' | 'pending' | 'error' = 'success') {
  vi.mocked(useRuns).mockReturnValue({
    total,
    status,
    runs: [],
  } as unknown as ReturnType<typeof useRuns>)
}

beforeEach(() => {
  mockRuns(0)
})

describe('DeleteAgentModal — rendering', () => {
  it('renders the modal title', () => {
    renderModal()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('Delete agent?')).toBeInTheDocument()
  })

  it('shows the agent name in the message', () => {
    renderModal({ policyName: 'deploy-on-push' })
    expect(screen.getByText('deploy-on-push')).toBeInTheDocument()
  })

  it('shows "no runs in audit trail" when run count is 0', () => {
    mockRuns(0)
    renderModal()
    expect(screen.getByText(/no runs in audit trail/)).toBeInTheDocument()
  })

  it('shows singular run count when there is 1 run', () => {
    mockRuns(1)
    renderModal()
    expect(screen.getByText(/1 run/)).toBeInTheDocument()
    expect(screen.queryByText(/1 runs/)).not.toBeInTheDocument()
  })

  it('shows plural run count when there are multiple runs', () => {
    mockRuns(3)
    renderModal()
    expect(screen.getByText(/3 runs/)).toBeInTheDocument()
  })

  it('does not show confirm input when run count is below threshold', () => {
    mockRuns(4)
    renderModal()
    expect(screen.queryByLabelText(/Type/)).not.toBeInTheDocument()
  })

  it('shows confirm input when run count meets threshold (5)', () => {
    mockRuns(5)
    renderModal()
    expect(screen.getByLabelText(/Type/)).toBeInTheDocument()
  })

  it('shows confirm input for large run counts', () => {
    mockRuns(100)
    renderModal()
    expect(screen.getByLabelText(/Type/)).toBeInTheDocument()
  })
})

describe('DeleteAgentModal — confirm name gating', () => {
  it('Delete agent button is enabled when run count is below threshold', () => {
    mockRuns(4)
    renderModal()
    const deleteBtn = screen.getByRole('button', { name: 'Delete agent' })
    expect(deleteBtn).not.toBeDisabled()
  })

  it('Delete agent button is disabled when name has not been typed', () => {
    mockRuns(5)
    renderModal({ policyName: 'my-agent' })
    const deleteBtn = screen.getByRole('button', { name: 'Delete agent' })
    expect(deleteBtn).toBeDisabled()
  })

  it('Delete agent button is disabled when partial name is typed', async () => {
    mockRuns(5)
    renderModal({ policyName: 'my-agent' })
    const input = screen.getByLabelText(/Type/)
    await userEvent.type(input, 'my-age')
    const deleteBtn = screen.getByRole('button', { name: 'Delete agent' })
    expect(deleteBtn).toBeDisabled()
  })

  it('Delete agent button becomes enabled when exact name is typed', async () => {
    mockRuns(5)
    renderModal({ policyName: 'my-agent' })
    const input = screen.getByLabelText(/Type/)
    await userEvent.type(input, 'my-agent')
    const deleteBtn = screen.getByRole('button', { name: 'Delete agent' })
    expect(deleteBtn).not.toBeDisabled()
  })

  it('Delete agent button is disabled again if name is changed after being correct', async () => {
    mockRuns(5)
    renderModal({ policyName: 'my-agent' })
    const input = screen.getByLabelText(/Type/)
    await userEvent.type(input, 'my-agent')
    await userEvent.type(input, 'x') // append extra char
    const deleteBtn = screen.getByRole('button', { name: 'Delete agent' })
    expect(deleteBtn).toBeDisabled()
  })
})

describe('DeleteAgentModal — interactions', () => {
  it('calls onConfirm when Delete agent button is clicked (no threshold)', () => {
    mockRuns(0)
    const onConfirm = vi.fn()
    renderModal({ onConfirm })
    fireEvent.click(screen.getByRole('button', { name: 'Delete agent' }))
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('calls onClose when Cancel is clicked', () => {
    mockRuns(0)
    const onClose = vi.fn()
    renderModal({ onClose })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('calls onClose when Escape key is pressed', () => {
    mockRuns(0)
    const onClose = vi.fn()
    renderModal({ onClose })
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('DeleteAgentModal — error display', () => {
  it('shows error message when error prop is set', () => {
    mockRuns(0)
    const error = { message: 'Delete failed', detail: 'Server error', status: 500, name: 'ApiError' } as never
    renderModal({ error })
    expect(screen.getByRole('alert')).toHaveTextContent('Server error')
  })

  it('falls back to error.message when detail is absent', () => {
    mockRuns(0)
    const error = { message: 'Something went wrong', status: 500, name: 'ApiError' } as never
    renderModal({ error })
    expect(screen.getByRole('alert')).toHaveTextContent('Something went wrong')
  })
})

describe('DeleteAgentModal — pending state', () => {
  it('shows loading label when isPending is true', async () => {
    mockRuns(0)
    renderModal({ isPending: true })
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /Deleting/ })).toBeInTheDocument()
    })
  })
})

describe('DeleteAgentModal — accessibility', () => {
  it('has role="dialog" and aria-modal="true"', () => {
    mockRuns(0)
    renderModal()
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
  })

  it('dialog is labelled by the title', () => {
    mockRuns(0)
    renderModal()
    const dialog = screen.getByRole('dialog')
    const labelledBy = dialog.getAttribute('aria-labelledby')
    expect(labelledBy).toBeTruthy()
    const title = document.getElementById(labelledBy!)
    expect(title).toHaveTextContent('Delete agent?')
  })
})

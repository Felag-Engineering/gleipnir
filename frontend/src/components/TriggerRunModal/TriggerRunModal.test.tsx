import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { TriggerRunModal } from './TriggerRunModal'
import { ApiError } from '@/api/fetch'

vi.mock('@/hooks/mutations/policies')

import { useTriggerPolicy } from '@/hooks/mutations/policies'

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderModal(ui: React.ReactElement) {
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('TriggerRunModal — launch failure surface', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders error.detail and a deep link to the failed run when ApiError carries runId', () => {
    const apiErr = new ApiError(
      500,
      'failed to launch run',
      'tool "ghost-server.foo" not found in registry',
      undefined,
      '01HG7Z9NWDRX0000000000',
    )
    vi.mocked(useTriggerPolicy).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      error: apiErr,
    } as unknown as ReturnType<typeof useTriggerPolicy>)

    renderModal(
      <TriggerRunModal policyId="p1" policyName="Test" onClose={() => {}} onSuccess={() => {}} />,
    )

    expect(screen.getByText('failed to launch run')).toBeInTheDocument()
    expect(screen.getByText(/ghost-server\.foo/)).toBeInTheDocument()

    const link = screen.getByRole('link', { name: /view failed run/i })
    expect(link).toHaveAttribute('href', '/runs/01HG7Z9NWDRX0000000000')
  })

  it('omits the deep link when ApiError has no runId (pre-CreateRun failure)', () => {
    const apiErr = new ApiError(
      500,
      'failed to launch run',
      'create run for policy abc: database is locked',
    )
    vi.mocked(useTriggerPolicy).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      error: apiErr,
    } as unknown as ReturnType<typeof useTriggerPolicy>)

    renderModal(
      <TriggerRunModal policyId="p1" policyName="Test" onClose={() => {}} onSuccess={() => {}} />,
    )

    // The detail still shows.
    expect(screen.getByText(/database is locked/)).toBeInTheDocument()
    // But there's no run link to follow.
    expect(screen.queryByRole('link', { name: /view failed run/i })).not.toBeInTheDocument()
  })

  it('clicking the failed-run link closes the modal so the page can navigate cleanly', async () => {
    const onClose = vi.fn()
    const apiErr = new ApiError(
      500,
      'failed to launch run',
      'tool not found',
      undefined,
      '01HG7Z9NWDRX0000000000',
    )
    vi.mocked(useTriggerPolicy).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      error: apiErr,
    } as unknown as ReturnType<typeof useTriggerPolicy>)

    renderModal(
      <TriggerRunModal policyId="p1" policyName="Test" onClose={onClose} onSuccess={() => {}} />,
    )

    const link = screen.getByRole('link', { name: /view failed run/i })
    await userEvent.click(link)
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })
})

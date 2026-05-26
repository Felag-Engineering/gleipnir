import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

import PluginReviewPage from './PluginReviewPage'
import type { ApiPluginDetail } from '@/api/types'

// --- Module mocks ---

vi.mock('@/hooks/queries/plugins')
vi.mock('@/hooks/mutations/plugins')

import { usePluginDetail } from '@/hooks/queries/plugins'
import { useApprovePlugin, useRejectPlugin } from '@/hooks/mutations/plugins'

// --- Fixtures ---

const PLUGIN_ID = 'plugin-slack-01'

const FULL_PLUGIN: ApiPluginDetail = {
  id: PLUGIN_ID,
  name: 'Slack',
  version: '1.2.0',
  description: 'Sends Slack messages and listens for events.',
  author: 'Gleipnir Labs',
  license: 'MIT',
  status: 'pending_review',
  services: ['tool', 'trigger', 'channel'],
  tier2_capabilities: ['run_history_read'],
  auth_strategy: 'oauth2_authcode',
  has_oauth_defaults: true,
  pubkey_fingerprint: 'a1b2c3d4e5f60001',
  has_sbom: true,
  created_at: '2025-05-01T10:00:00Z',
}

// --- Helpers ---

function mockPluginDetail(plugin: ApiPluginDetail | undefined, status: 'success' | 'pending' | 'error' = 'success') {
  vi.mocked(usePluginDetail).mockReturnValue({
    data: plugin,
    status,
    isLoading: status === 'pending',
    isError: status === 'error',
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof usePluginDetail>)
}

function mockApprove(mutate = vi.fn()) {
  vi.mocked(useApprovePlugin).mockReturnValue({
    mutate,
    isPending: false,
    isError: false,
    isSuccess: false,
  } as unknown as ReturnType<typeof useApprovePlugin>)
}

function mockReject(mutate = vi.fn()) {
  vi.mocked(useRejectPlugin).mockReturnValue({
    mutate,
    isPending: false,
    isError: false,
    isSuccess: false,
  } as unknown as ReturnType<typeof useRejectPlugin>)
}

function renderPage(pluginId = PLUGIN_ID) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[`/admin/plugins/${pluginId}/review`]}>
        <Routes>
          <Route path="/admin/plugins/:id/review" element={<PluginReviewPage />} />
          <Route path="/admin/plugins" element={<div>Plugins list</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// --- Tests ---

describe('PluginReviewPage', () => {
  beforeEach(() => {
    mockPluginDetail(FULL_PLUGIN)
    mockApprove()
    mockReject()
  })

  it('renders the plugin name, version, and description', () => {
    renderPage()

    expect(screen.getByText('Slack')).toBeInTheDocument()
    expect(screen.getByText('1.2.0')).toBeInTheDocument()
    expect(screen.getByText('Sends Slack messages and listens for events.')).toBeInTheDocument()
  })

  it('renders service badges', () => {
    renderPage()

    expect(screen.getByText('Tool')).toBeInTheDocument()
    expect(screen.getByText('Trigger')).toBeInTheDocument()
    expect(screen.getByText('Channel')).toBeInTheDocument()
  })

  it('renders tier-2 capability badges', () => {
    renderPage()

    expect(screen.getByText('run_history_read')).toBeInTheDocument()
  })

  it('renders auth strategy label', () => {
    renderPage()

    // The human-readable label for oauth2_authcode
    expect(screen.getByText(/OAuth 2\.0 \(authorization code\)/i)).toBeInTheDocument()
  })

  it('renders the pubkey fingerprint', () => {
    renderPage()

    expect(screen.getByText('a1b2c3d4e5f60001')).toBeInTheDocument()
  })

  it('renders SBOM status', () => {
    renderPage()

    expect(screen.getByText('Included')).toBeInTheDocument()
  })

  it('renders Approve and Reject buttons', () => {
    renderPage()

    expect(screen.getByRole('button', { name: 'Approve' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reject' })).toBeInTheDocument()
  })

  it('calls approve mutation when Approve is clicked', async () => {
    const mutate = vi.fn()
    mockApprove(mutate)
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: 'Approve' }))

    expect(mutate).toHaveBeenCalledWith(
      { pluginId: PLUGIN_ID },
      expect.objectContaining({ onSuccess: expect.any(Function) }),
    )
  })

  it('opens confirmation modal when Reject is clicked', async () => {
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: 'Reject' }))

    // Modal should appear; mutation should NOT be called yet.
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText(/reject and delete/i)).toBeInTheDocument()
  })

  it('calls reject mutation only after modal confirmation', async () => {
    const mutate = vi.fn()
    mockReject(mutate)
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: 'Reject' }))

    // Mutation not called while modal is open.
    expect(mutate).not.toHaveBeenCalled()

    await userEvent.click(screen.getByRole('button', { name: 'Reject plugin' }))

    expect(mutate).toHaveBeenCalledWith(
      { pluginId: PLUGIN_ID },
      expect.objectContaining({ onSuccess: expect.any(Function) }),
    )
  })

  it('closes modal without calling mutation when Cancel is clicked', async () => {
    const mutate = vi.fn()
    mockReject(mutate)
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: 'Reject' }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(mutate).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('shows a banner when the plugin is not in pending_review status', () => {
    mockPluginDetail({ ...FULL_PLUGIN, status: 'active' })
    renderPage()

    expect(screen.getByText(/no longer pending review/i)).toBeInTheDocument()
  })

  it('renders a Back to plugins link', () => {
    renderPage()

    const link = screen.getByRole('link', { name: /back to plugins/i })
    expect(link).toHaveAttribute('href', '/admin/plugins')
  })

  it('navigates to plugins page after successful approve', async () => {
    const mutate = vi.fn().mockImplementation((_params, { onSuccess }) => onSuccess())
    mockApprove(mutate)
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: 'Approve' }))

    await waitFor(() => {
      expect(screen.getByText('Plugins list')).toBeInTheDocument()
    })
  })

  it('navigates to plugins page after successful reject confirmation', async () => {
    const mutate = vi.fn().mockImplementation((_params, { onSuccess }) => onSuccess())
    mockReject(mutate)
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: 'Reject' }))
    await userEvent.click(screen.getByRole('button', { name: 'Reject plugin' }))

    await waitFor(() => {
      expect(screen.getByText('Plugins list')).toBeInTheDocument()
    })
  })
})

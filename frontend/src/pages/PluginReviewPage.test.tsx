import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router'

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
  manifest_version: 1,
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

  it('renders SBOM status as a link to the SBOM endpoint', () => {
    renderPage()

    const link = screen.getByRole('link', { name: 'Included' })
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute(
      'href',
      `/api/v1/admin/plugins/${encodeURIComponent(PLUGIN_ID)}/sbom`,
    )
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

describe('PluginReviewPage v2 consent', () => {
  const V2_PLUGIN: ApiPluginDetail = {
    id: PLUGIN_ID,
    name: 'Acme Notify',
    version: '1.0.0',
    status: 'pending_review',
    services: [],
    tier2_capabilities: ['user_directory_read'],
    auth_strategy: 'static_api_key',
    has_oauth_defaults: false,
    pubkey_fingerprint: 'feedfacecafe0001',
    has_sbom: false,
    manifest_version: 2,
    row_version: 5,
    created_at: '2026-01-01T00:00:00Z',
    v2: {
      image: {
        identifier: 'ghcr.io/acme/notify@sha256:abc123',
        repository: 'ghcr.io/acme/notify',
        digest: 'sha256:abc123',
        registry_type: 'oci',
        transport_type: 'streamable-http',
        transport_port: 8080,
      },
      egress: [{ domain: 'api.acme.example', reason: 'calls the Acme API' }],
      resources: { memory_mb: 256, cpu_millicores: 500 },
      effective_resources: {
        memory_mb: 256,
        cpu_millicores: 500,
        memory_source: 'manifest',
        cpu_source: 'manifest',
      },
      profiles: {
        declared: ['tool_provider', 'human_channel'],
        human_channel_assurance: 'weak',
      },
      tools: [{ name: 'delete_all', elicitation_kind: 'permission' }],
      event_kinds: [],
      auth: { strategy: 'static_api_key', header_name: 'X-Api-Key' },
      config_schema: { type: 'object', properties: { workspace: { type: 'string' } } },
    },
  }

  beforeEach(() => {
    mockApprove()
    mockReject()
  })

  it('renders image, digest, egress, resources, profiles, tools, auth and config schema', () => {
    mockPluginDetail(V2_PLUGIN)
    renderPage()

    expect(screen.getByText('ghcr.io/acme/notify')).toBeInTheDocument()
    expect(screen.getByText('sha256:abc123')).toBeInTheDocument()
    expect(screen.getByText('api.acme.example')).toBeInTheDocument()
    expect(screen.getByText('calls the Acme API')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Network access' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Resource limits' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Capabilities' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Image' })).toBeInTheDocument()
    expect(screen.getByText('256 MiB')).toBeInTheDocument()
    expect(screen.getByText('500 millicores')).toBeInTheDocument()
    expect(screen.getByText('Tool provider')).toBeInTheDocument()
    expect(screen.getByText('Human channel')).toBeInTheDocument()
    expect(screen.getByText('Channel assurance: weak')).toBeInTheDocument()
    expect(screen.getByText('delete_all')).toBeInTheDocument()
    expect(screen.getByText('May ask for your permission')).toBeInTheDocument()
    expect(screen.getByText('Header: X-Api-Key')).toBeInTheDocument()
    expect(screen.getByText(/"workspace"/)).toBeInTheDocument()
    // Tier-2 capabilities and the signing key are shown once, not duplicated.
    expect(screen.getAllByText('user_directory_read')).toHaveLength(1)
    expect(screen.getAllByText('feedfacecafe0001')).toHaveLength(1)
  })

  it('says so when the plugin requests no egress and declares no resources', () => {
    mockPluginDetail({
      ...V2_PLUGIN,
      v2: {
        ...V2_PLUGIN.v2!,
        egress: [],
        resources: null,
        effective_resources: {
          memory_mb: 256,
          cpu_millicores: 500,
          memory_source: 'default',
          cpu_source: 'default',
        },
      },
    })
    renderPage()

    expect(screen.getByRole('heading', { name: 'No internet access' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Network access' })).not.toBeInTheDocument()
    expect(screen.getAllByText('(host default)')).toHaveLength(2)
  })

  it('shows tool permission asks without protocol terms', () => {
    mockPluginDetail({
      ...V2_PLUGIN,
      v2: {
        ...V2_PLUGIN.v2!,
        tools: [
          { name: 'ask_user', elicitation_kind: 'information' },
          { name: 'plain_tool' },
        ],
      },
    })
    renderPage()

    expect(screen.getByText('May ask you for information')).toBeInTheDocument()
    expect(screen.queryByText(/elicitation/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/mcp/i)).not.toBeInTheDocument()
  })

  it('flags an unsigned v2 bundle', () => {
    mockPluginDetail({ ...V2_PLUGIN, pubkey_fingerprint: undefined })
    renderPage()

    expect(screen.getByText(/Unsigned bundle/)).toBeInTheDocument()
  })

  it('does not render the v2 section for a v1 plugin', () => {
    mockPluginDetail(FULL_PLUGIN)
    renderPage()

    expect(screen.queryByText('Network egress')).not.toBeInTheDocument()
  })

  it('approves a v2 plugin through the same Approve button', async () => {
    const mutate = vi.fn()
    mockApprove(mutate)
    mockPluginDetail(V2_PLUGIN)
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: 'Approve' }))

    expect(mutate).toHaveBeenCalledWith(
      { pluginId: PLUGIN_ID, expectedVersion: 5 },
      expect.anything(),
    )
  })

  it('renders hostile manifest text literally, creating no elements', () => {
    const hostile = '<img src=x onerror=alert(1)>'
    mockPluginDetail({
      ...V2_PLUGIN,
      v2: {
        ...V2_PLUGIN.v2!,
        egress: [{ domain: 'api.acme.example', reason: hostile }],
        tools: [{ name: hostile, elicitation_kind: 'permission' }],
      },
    })
    const { container } = renderPage()

    expect(screen.getAllByText(hostile).length).toBeGreaterThan(0)
    expect(container.querySelector('img')).toBeNull()
  })

  it('omits the signing key row for an unsigned bundle', () => {
    mockPluginDetail({ ...V2_PLUGIN, pubkey_fingerprint: undefined })
    renderPage()

    expect(screen.queryByText('feedfacecafe0001')).not.toBeInTheDocument()
    expect(screen.getByText(/Unsigned bundle/)).toBeInTheDocument()
  })
})

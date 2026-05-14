import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

import AdminPluginInstancePage from './AdminPluginInstancePage'
import type { ApiPluginInstanceForAudience } from '@/api/types'

// --- Module mocks ---

vi.mock('@/hooks/queries/admin')
vi.mock('@/hooks/mutations/plugins')

import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { useSetInstanceSubscriptionScope } from '@/hooks/mutations/plugins'

// --- Fixtures ---

const PLUGIN_ID = 'plugin-slack-01'
const INSTANCE_ID = 'inst-slack-prod'

const INSTANCE_WITH_SCHEMA: ApiPluginInstanceForAudience = {
  id: INSTANCE_ID,
  plugin_id: PLUGIN_ID,
  plugin_name: 'Slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  event_kinds: [],
  subscription_schema: {
    type: 'object',
    properties: {
      channels: {
        type: 'array',
        items: { type: 'string' },
      },
    },
  },
  subscription_scope: { channels: ['#incidents'] },
  version: 1,
}

const INSTANCE_NO_SCHEMA: ApiPluginInstanceForAudience = {
  id: INSTANCE_ID,
  plugin_id: PLUGIN_ID,
  plugin_name: 'Webhook',
  instance_name: 'webhook-prod',
  state: 'healthy',
  implements_notify: false,
  implements_request: false,
  config_schema: null,
  event_kinds: [],
  version: 0,
}

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderPage() {
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter initialEntries={[`/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}`]}>
        <Routes>
          <Route path="/admin/plugins/:id/instances/:iid" element={<AdminPluginInstancePage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function mockInstancesLoaded(instances: ApiPluginInstanceForAudience[]) {
  vi.mocked(usePluginInstancesForAudience).mockReturnValue({
    data: instances,
    status: 'success',
  } as unknown as ReturnType<typeof usePluginInstancesForAudience>)
}

function mockInstancesPending() {
  vi.mocked(usePluginInstancesForAudience).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as unknown as ReturnType<typeof usePluginInstancesForAudience>)
}

function mockMutationNoop() {
  vi.mocked(useSetInstanceSubscriptionScope).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useSetInstanceSubscriptionScope>)
}

function mockMutationSuccess(onSuccessCapture?: (fn: () => void) => void) {
  const mutateFn = vi.fn((_params, opts) => {
    if (opts?.onSuccess) {
      opts.onSuccess()
      onSuccessCapture?.(opts.onSuccess)
    }
  })
  vi.mocked(useSetInstanceSubscriptionScope).mockReturnValue({
    mutate: mutateFn,
    isPending: false,
  } as unknown as ReturnType<typeof useSetInstanceSubscriptionScope>)
  return mutateFn
}

// --- Tests ---

describe('AdminPluginInstancePage — loading state', () => {
  beforeEach(() => {
    mockInstancesPending()
    mockMutationNoop()
  })

  it('shows loading message while data is pending', () => {
    renderPage()
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })
})

describe('AdminPluginInstancePage — with subscription_schema', () => {
  beforeEach(() => {
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    mockMutationNoop()
  })

  it('renders the Subscriptions tab', () => {
    renderPage()
    expect(screen.getByRole('button', { name: /subscriptions/i })).toBeInTheDocument()
  })

  it('renders schema fields on the Subscriptions tab', () => {
    renderPage()
    // The "channels" field should appear as a textarea (string[] type).
    expect(screen.getByLabelText(/channels/i)).toBeInTheDocument()
  })

  it('renders the Save scope button', () => {
    renderPage()
    expect(screen.getByRole('button', { name: /save scope/i })).toBeInTheDocument()
  })

  it('calls mutation with scope on save', () => {
    const mutateFn = mockMutationSuccess()
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /save scope/i }))
    expect(mutateFn).toHaveBeenCalledTimes(1)
    const call = mutateFn.mock.calls[0][0]
    expect(call.pluginId).toBe(PLUGIN_ID)
    expect(call.instanceId).toBe(INSTANCE_ID)
  })

  it('shows saved confirmation after successful save', async () => {
    mockMutationSuccess()
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /save scope/i }))
    await waitFor(() => {
      expect(screen.getByText(/saved/i)).toBeInTheDocument()
    })
  })
})

describe('AdminPluginInstancePage — without subscription_schema', () => {
  beforeEach(() => {
    mockInstancesLoaded([INSTANCE_NO_SCHEMA])
    mockMutationNoop()
  })

  it('does NOT render the Subscriptions tab when schema is absent', () => {
    renderPage()
    expect(screen.queryByRole('button', { name: /subscriptions/i })).not.toBeInTheDocument()
  })

  it('renders Config and Credentials tabs as placeholders', () => {
    renderPage()
    expect(screen.getByRole('button', { name: /config/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /credentials/i })).toBeInTheDocument()
  })
})

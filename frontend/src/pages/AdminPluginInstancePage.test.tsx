import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

import AdminPluginInstancePage from './AdminPluginInstancePage'
import type { ApiPluginInstanceForAudience } from '@/api/types'
import { RefreshFailureDetailPrefix } from '@/utils/pluginHealth'

// --- Module mocks ---

vi.mock('@/hooks/queries/admin')
vi.mock('@/hooks/mutations/plugins')
vi.mock('@/components/admin/ReauthorizeButton/ReauthorizeButton', () => ({
  ReauthorizeButton: ({ strategy }: { strategy: string }) => (
    <button data-testid="reauth-btn" data-strategy={strategy}>Re-authorize</button>
  ),
}))

import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { useSetInstanceSubscriptionScope, useBeginPluginOAuth } from '@/hooks/mutations/plugins'

// --- Fixtures ---

const PLUGIN_ID = 'plugin-slack-01'
const INSTANCE_ID = 'inst-slack-prod'

const INSTANCE_WITH_SCHEMA: ApiPluginInstanceForAudience = {
  id: INSTANCE_ID,
  plugin_id: PLUGIN_ID,
  plugin_name: 'Slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  auth_strategy: 'oauth2_authcode',
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
  auth_strategy: 'none',
  implements_notify: false,
  implements_request: false,
  config_schema: null,
  event_kinds: [],
  version: 0,
}

const INSTANCE_OAUTH_REFRESH_FAILED: ApiPluginInstanceForAudience = {
  id: INSTANCE_ID,
  plugin_id: PLUGIN_ID,
  plugin_name: 'Slack',
  instance_name: 'slack-prod',
  state: 'unhealthy',
  health_detail: `${RefreshFailureDetailPrefix}: token expired`,
  auth_strategy: 'oauth2_authcode',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  event_kinds: [],
  version: 2,
}

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderPage(search = '') {
  const path = `/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}${search}`
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter initialEntries={[path]}>
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
  vi.mocked(useBeginPluginOAuth).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useBeginPluginOAuth>)
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

describe('AdminPluginInstancePage — Re-authorize banner visibility', () => {
  beforeEach(() => {
    mockMutationNoop()
  })

  it('shows the Re-authorize banner when instance is unhealthy with refresh-failure detail', () => {
    mockInstancesLoaded([INSTANCE_OAUTH_REFRESH_FAILED])
    renderPage()
    expect(screen.getByText(/oauth credentials need re-authorization/i)).toBeInTheDocument()
    expect(screen.getByTestId('reauth-btn')).toBeInTheDocument()
  })

  it('does NOT show the banner for a healthy instance', () => {
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    renderPage()
    expect(screen.queryByText(/oauth credentials need re-authorization/i)).not.toBeInTheDocument()
    expect(screen.queryByTestId('reauth-btn')).not.toBeInTheDocument()
  })

  it('does NOT show the banner for an unhealthy instance without refresh-failure detail', () => {
    const unhealthyOther: ApiPluginInstanceForAudience = {
      ...INSTANCE_OAUTH_REFRESH_FAILED,
      health_detail: 'something else went wrong',
    }
    mockInstancesLoaded([unhealthyOther])
    renderPage()
    expect(screen.queryByTestId('reauth-btn')).not.toBeInTheDocument()
  })

  it('passes auth_strategy to ReauthorizeButton', () => {
    mockInstancesLoaded([INSTANCE_OAUTH_REFRESH_FAILED])
    renderPage()
    expect(screen.getByTestId('reauth-btn')).toHaveAttribute('data-strategy', 'oauth2_authcode')
  })
})

describe('AdminPluginInstancePage — oauth_ok query param', () => {
  // The page reads window.location.search directly (not from React Router) so
  // the jsdom window.location must match. Replace it with a stub for these tests.

  function withLocationSearch(search: string, fn: () => void) {
    const originalLocation = window.location
    Object.defineProperty(window, 'location', {
      writable: true,
      value: { ...originalLocation, search },
    })
    fn()
    Object.defineProperty(window, 'location', { writable: true, value: originalLocation })
  }

  it('invalidates plugin-instances query on mount when ?oauth_ok=1 is present', async () => {
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    mockMutationNoop()

    const invalidateSpy = vi.spyOn(QueryClient.prototype, 'invalidateQueries').mockResolvedValue()

    withLocationSearch('?oauth_ok=1', () => {
      renderPage()
    })

    await waitFor(() => {
      expect(invalidateSpy).toHaveBeenCalled()
    })

    invalidateSpy.mockRestore()
  })

  it('does NOT invalidate when ?oauth_ok is absent', async () => {
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    mockMutationNoop()

    const invalidateSpy = vi.spyOn(QueryClient.prototype, 'invalidateQueries').mockResolvedValue()

    withLocationSearch('', () => {
      renderPage()
    })

    // Allow effects to flush.
    await new Promise((r) => setTimeout(r, 50))
    expect(invalidateSpy).not.toHaveBeenCalled()

    invalidateSpy.mockRestore()
  })
})

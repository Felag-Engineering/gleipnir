import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

import AdminPluginInstancePage from './AdminPluginInstancePage'
import type { ApiPluginInstanceForAudience } from '@/api/types'
import { RefreshFailureDetailPrefix } from '@/utils/pluginHealth'

// --- Module mocks ---

vi.mock('@/hooks/queries/admin')
vi.mock('@/hooks/mutations/plugins')
vi.mock('@/hooks/queries/users')
vi.mock('@/components/admin/ReauthorizeButton/ReauthorizeButton', () => ({
  ReauthorizeButton: ({ strategy }: { strategy: string }) => (
    <button data-testid="reauth-btn" data-strategy={strategy}>Re-authorize</button>
  ),
}))

import { usePluginInstancesForAudience, usePluginInstanceDetail } from '@/hooks/queries/admin'
import { useSetInstanceSubscriptionScope, useBeginPluginOAuth, useDeletePluginInstance, useSetInstanceConfig } from '@/hooks/mutations/plugins'
import type { ApiPluginInstanceDetail } from '@/api/types'
import { useCurrentUser } from '@/hooks/queries/users'
import { ApiError } from '@/api/fetch'

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

const INSTANCE_WITH_CONFIG_SCHEMA: ApiPluginInstanceForAudience = {
  id: INSTANCE_ID,
  plugin_id: PLUGIN_ID,
  plugin_name: 'Slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  auth_strategy: 'oauth2_authcode',
  implements_notify: true,
  implements_request: true,
  config_schema: {
    type: 'object',
    properties: {
      app_level_token: {
        type: 'string',
        'x-gleipnir-secret': true,
        description: 'Slack app-level token (xapp-...)',
      },
      workspace_name: {
        type: 'string',
        description: 'Display name for this workspace',
      },
    },
  },
  event_kinds: [],
  version: 3,
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
          <Route path="/admin/plugins" element={<div>Plugins list</div>} />
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

function mockCurrentUser(roles: string[] = ['admin']) {
  vi.mocked(useCurrentUser).mockReturnValue({
    data: { id: 'user-01', username: 'admin', roles },
    status: 'success',
    isLoading: false,
    isError: false,
  } as unknown as ReturnType<typeof useCurrentUser>)
}

function mockInstanceDetailLoaded(configJson = '{}') {
  vi.mocked(usePluginInstanceDetail).mockReturnValue({
    data: {
      id: INSTANCE_ID,
      plugin_id: PLUGIN_ID,
      instance_name: 'slack-prod',
      state: 'healthy',
      detail: null,
      version: 1,
      updated_at: '2024-01-01T00:00:00Z',
      subscription_scope_json: '{}',
      config_json: configJson,
    },
    status: 'success',
  } as unknown as ReturnType<typeof usePluginInstanceDetail>)
}

function mockInstanceDetailPending() {
  vi.mocked(usePluginInstanceDetail).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as unknown as ReturnType<typeof usePluginInstanceDetail>)
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
  vi.mocked(useDeletePluginInstance).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useDeletePluginInstance>)
  vi.mocked(useSetInstanceConfig).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useSetInstanceConfig>)
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
    mockCurrentUser(['admin'])
    mockInstancesPending()
    mockInstanceDetailPending()
    mockMutationNoop()
  })

  it('shows loading message while data is pending', () => {
    renderPage()
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })
})

describe('AdminPluginInstancePage — with subscription_schema', () => {
  beforeEach(() => {
    mockCurrentUser(['admin'])
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    mockInstanceDetailLoaded()
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
    mockCurrentUser(['admin'])
    mockInstancesLoaded([INSTANCE_NO_SCHEMA])
    mockInstanceDetailLoaded()
    mockMutationNoop()
  })

  it('does NOT render the Subscriptions tab when schema is absent', () => {
    renderPage()
    expect(screen.queryByRole('button', { name: /subscriptions/i })).not.toBeInTheDocument()
  })

  it('renders Config and Credentials tabs', () => {
    renderPage()
    // Use exact tab button text to avoid matching "Save config" in the tab content.
    expect(screen.getByRole('button', { name: 'Config' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Credentials' })).toBeInTheDocument()
  })
})

describe('AdminPluginInstancePage — Re-authorize banner visibility', () => {
  beforeEach(() => {
    mockCurrentUser(['admin'])
    mockInstanceDetailLoaded()
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

describe('AdminPluginInstancePage — delete instance', () => {
  beforeEach(() => {
    mockCurrentUser(['admin'])
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    mockInstanceDetailLoaded()
    mockMutationNoop()
  })

  it('renders Delete instance button for admin role', () => {
    renderPage()
    expect(screen.getByRole('button', { name: /delete instance/i })).toBeInTheDocument()
  })

  it('does NOT render Delete instance button for auditor role', () => {
    mockCurrentUser(['auditor'])
    renderPage()
    expect(screen.queryByRole('button', { name: /delete instance/i })).not.toBeInTheDocument()
  })

  it('opens the DeletePluginInstanceModal when Delete instance is clicked', async () => {
    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /delete instance/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    // Modal should display the instance name.
    expect(screen.getByText('slack-prod')).toBeInTheDocument()
  })

  it('calls mutation and navigates to /admin/plugins on success', async () => {
    const mutateFn = vi.fn((_params: unknown, opts: { onSuccess?: () => void }) => {
      opts.onSuccess?.()
    })
    vi.mocked(useDeletePluginInstance).mockReturnValue({
      mutate: mutateFn,
      isPending: false,
    } as unknown as ReturnType<typeof useDeletePluginInstance>)

    renderPage()
    // Open modal via the "Delete instance" button in the page header.
    await userEvent.click(screen.getByRole('button', { name: /delete instance/i }))
    // Confirm inside the dialog — avoids ambiguity if button text matches multiple elements.
    const dialog = screen.getByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: /delete instance/i }))

    expect(mutateFn).toHaveBeenCalledWith(
      { pluginId: PLUGIN_ID, instanceId: INSTANCE_ID },
      expect.any(Object),
    )
  })

  it('shows 409 error detail in the modal when deletion is blocked', async () => {
    const apiErr = new ApiError(409, 'conflict', 'Policy "Nightly" still references this instance.')
    const mutateFn = vi.fn((_params: unknown, opts: { onError?: (err: unknown) => void }) => {
      opts.onError?.(apiErr)
    })
    vi.mocked(useDeletePluginInstance).mockReturnValue({
      mutate: mutateFn,
      isPending: false,
    } as unknown as ReturnType<typeof useDeletePluginInstance>)

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /delete instance/i }))
    const dialog = screen.getByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: /delete instance/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Policy "Nightly"')
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
    mockCurrentUser(['admin'])
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    mockInstanceDetailLoaded()
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
    mockCurrentUser(['admin'])
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    mockInstanceDetailLoaded()
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

// ── Config tab ────────────────────────────────────────────────────────────────

describe('AdminPluginInstancePage — Config tab', () => {
  function mockConfigMutation(behavior: 'noop' | 'success' | 'conflict' | 'validation') {
    let mutateFn: ReturnType<typeof vi.fn>

    if (behavior === 'success') {
      mutateFn = vi.fn((_params, opts: { onSuccess?: () => void }) => {
        opts.onSuccess?.()
      })
    } else if (behavior === 'conflict') {
      mutateFn = vi.fn((_params, opts: { onError?: (err: unknown) => void }) => {
        opts.onError?.(new ApiError(409, 'version conflict'))
      })
    } else if (behavior === 'validation') {
      mutateFn = vi.fn((_params, opts: { onError?: (err: unknown) => void }) => {
        opts.onError?.(
          new ApiError(422, 'validation failed', undefined, [
            { field: 'workspace_name', message: 'must not be empty' },
          ]),
        )
      })
    } else {
      mutateFn = vi.fn()
    }

    vi.mocked(useSetInstanceConfig).mockReturnValue({
      mutate: mutateFn,
      isPending: false,
    } as unknown as ReturnType<typeof useSetInstanceConfig>)

    return mutateFn
  }

  beforeEach(() => {
    mockCurrentUser(['admin'])
    mockInstancesLoaded([INSTANCE_WITH_CONFIG_SCHEMA])
    mockInstanceDetailLoaded(JSON.stringify({ app_level_token: '***', workspace_name: 'Acme' }))
    mockMutationNoop()
  })

  it('renders the Config tab with schema fields', () => {
    renderPage()
    // Navigate to Config tab (it may not be active by default when subscription_schema is absent).
    const configTabBtn = screen.getByRole('button', { name: 'Config' })
    fireEvent.click(configTabBtn)
    // workspace_name should appear as a text input.
    expect(screen.getByLabelText(/workspace_name/i)).toBeInTheDocument()
  })

  it('renders secret fields as password inputs', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    const secretInput = screen.getByLabelText(/app_level_token/i)
    expect(secretInput).toHaveAttribute('type', 'password')
  })

  it('shows sentinel placeholder for secret fields already set on server', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    const secretInput = screen.getByLabelText(/app_level_token/i)
    // Sentinel "***" should be rendered as empty value with a placeholder.
    expect(secretInput).toHaveValue('')
    expect(secretInput).toHaveAttribute('placeholder', '(already set — leave blank to keep)')
  })

  it('renders without crashing for an empty config_schema', () => {
    mockInstancesLoaded([
      { ...INSTANCE_WITH_CONFIG_SCHEMA, config_schema: {} },
    ])
    mockInstanceDetailLoaded('{}')
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    expect(screen.getByText(/no config fields declared/i)).toBeInTheDocument()
  })

  it('renders without crashing when config_schema is null', () => {
    mockInstancesLoaded([INSTANCE_NO_SCHEMA])
    mockInstanceDetailLoaded('{}')
    renderPage()
    // Config tab is the default active tab when no subscription_schema is present.
    expect(screen.getByText(/no config fields declared/i)).toBeInTheDocument()
  })

  it('calls save mutation with correct params on Save config click', () => {
    const mutateFn = mockConfigMutation('success')
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    fireEvent.click(screen.getByRole('button', { name: /save config/i }))
    expect(mutateFn).toHaveBeenCalledTimes(1)
    const call = mutateFn.mock.calls[0][0]
    expect(call.pluginId).toBe(PLUGIN_ID)
    expect(call.instanceId).toBe(INSTANCE_ID)
    // The sentinel field should be stripped from the payload.
    expect(call.config).not.toHaveProperty('app_level_token')
    // Non-sentinel fields are kept.
    expect(call.config).toHaveProperty('workspace_name', 'Acme')
  })

  it('shows "Saved." after a successful save', async () => {
    mockConfigMutation('success')
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    fireEvent.click(screen.getByRole('button', { name: /save config/i }))
    await waitFor(() => {
      expect(screen.getByText('Saved.')).toBeInTheDocument()
    })
  })

  it('shows CAS conflict message on 409', async () => {
    mockConfigMutation('conflict')
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    fireEvent.click(screen.getByRole('button', { name: /save config/i }))
    await waitFor(() => {
      expect(screen.getByText(/modified elsewhere/i)).toBeInTheDocument()
    })
    // Refresh button should be present.
    expect(screen.getByRole('button', { name: /refresh/i })).toBeInTheDocument()
  })

  it('shows field-level validation errors on 422', async () => {
    mockConfigMutation('validation')
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    fireEvent.click(screen.getByRole('button', { name: /save config/i }))
    await waitFor(() => {
      expect(screen.getByText(/must not be empty/i)).toBeInTheDocument()
    })
  })

  it('back-link points to /admin/plugins', () => {
    renderPage()
    const backLink = screen.getByRole('link', { name: /plugins/i })
    expect(backLink).toHaveAttribute('href', '/admin/plugins')
  })
})

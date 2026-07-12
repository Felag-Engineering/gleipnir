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
import { useSetInstanceSubscriptionScope, useBeginPluginOAuth, useDeletePluginInstance, useDeactivatePluginInstance, useActivatePluginInstance, useSetInstanceConfig, useAcceptPluginManifest } from '@/hooks/mutations/plugins'
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
  subscription_scope: { channels: ['C012ABCDEF'] },
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

// SLACK_CONFIG_SCHEMA is the instance-level config schema for the Slack-like
// manifest. The Config-tab tests supply it via the detail mock
// (mockInstanceDetailLoaded(..., SLACK_CONFIG_SCHEMA)) to match the fixed data
// flow where ConfigTab reads config_schema from usePluginInstanceDetail rather
// than from the listing endpoint's channel schema.
const SLACK_CONFIG_SCHEMA: Record<string, unknown> = {
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
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof usePluginInstancesForAudience>)
}

function mockInstancesPending() {
  vi.mocked(usePluginInstancesForAudience).mockReturnValue({
    data: undefined,
    status: 'pending',
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof usePluginInstancesForAudience>)
}

function mockInstancesError() {
  vi.mocked(usePluginInstancesForAudience).mockReturnValue({
    data: undefined,
    status: 'error',
    refetch: vi.fn(),
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

function mockInstanceDetailLoaded(
  configJson = '{}',
  configSchema: Record<string, unknown> | null = null,
) {
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
      config_schema: configSchema,
    } satisfies ApiPluginInstanceDetail,
    status: 'success',
  } as unknown as ReturnType<typeof usePluginInstanceDetail>)
}

function mockInstanceDetailPending() {
  vi.mocked(usePluginInstanceDetail).mockReturnValue({
    data: undefined,
    status: 'pending',
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof usePluginInstanceDetail>)
}

function mockInstanceDetailError() {
  vi.mocked(usePluginInstanceDetail).mockReturnValue({
    data: undefined,
    status: 'error',
    refetch: vi.fn(),
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
  vi.mocked(useAcceptPluginManifest).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useAcceptPluginManifest>)
  vi.mocked(useDeactivatePluginInstance).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useDeactivatePluginInstance>)
  vi.mocked(useActivatePluginInstance).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useActivatePluginInstance>)
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

  it('shows a skeleton while the instance list is pending', () => {
    const { container } = renderPage()
    // The shared SkeletonList renders aria-hidden skeleton blocks.
    expect(container.querySelectorAll('[aria-hidden="true"]').length).toBeGreaterThan(0)
    // No raw "Loading…" text remains.
    expect(screen.queryByText('Loading…')).not.toBeInTheDocument()
  })
})

describe('AdminPluginInstancePage — error state', () => {
  beforeEach(() => {
    mockCurrentUser(['admin'])
    mockInstanceDetailPending()
    mockMutationNoop()
  })

  it('shows a recoverable error with retry when the instance list fails to load', () => {
    mockInstancesError()
    renderPage()
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText(/instance could not be loaded/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })

  it('retry button calls refetch on the instance list', () => {
    const refetch = vi.fn()
    vi.mocked(usePluginInstancesForAudience).mockReturnValue({
      data: undefined,
      status: 'error',
      refetch,
    } as unknown as ReturnType<typeof usePluginInstancesForAudience>)
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(refetch).toHaveBeenCalledTimes(1)
  })
})

describe('AdminPluginInstancePage — Config tab error state', () => {
  beforeEach(() => {
    mockCurrentUser(['admin'])
    mockInstancesLoaded([INSTANCE_NO_SCHEMA])
    mockMutationNoop()
  })

  it('shows a recoverable error with retry when the config detail fails to load', () => {
    mockInstanceDetailError()
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    expect(screen.getByText(/could not load instance config/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
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

describe('AdminPluginInstancePage — Accept manifest modal', () => {
  const PENDING_MANIFEST_INSTANCE: ApiPluginInstanceForAudience = {
    ...INSTANCE_WITH_SCHEMA,
    state: 'pending_manifest_approval',
    health_detail: 'manifest changed materially; admin re-approval required',
  }

  beforeEach(() => {
    mockInstanceDetailLoaded()
    mockMutationNoop()
  })

  it('shows the banner with Review change button when instance is pending_manifest_approval', () => {
    mockCurrentUser(['admin'])
    mockInstancesLoaded([PENDING_MANIFEST_INSTANCE])
    renderPage()
    expect(screen.getByText(/new manifest version is waiting for approval/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /review change/i })).toBeInTheDocument()
  })

  it('hides the banner for non-admin users', () => {
    mockCurrentUser(['operator'])
    mockInstancesLoaded([PENDING_MANIFEST_INSTANCE])
    renderPage()
    expect(screen.queryByText(/new manifest version is waiting for approval/i)).not.toBeInTheDocument()
  })

  it('hides the banner for healthy instances', () => {
    mockCurrentUser(['admin'])
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    renderPage()
    expect(screen.queryByText(/new manifest version is waiting for approval/i)).not.toBeInTheDocument()
  })

  it('opens the modal and fires the accept-manifest mutation on confirm', async () => {
    mockCurrentUser(['admin'])
    mockInstancesLoaded([PENDING_MANIFEST_INSTANCE])
    const mutateFn = vi.fn()
    vi.mocked(useAcceptPluginManifest).mockReturnValue({
      mutate: mutateFn,
      isPending: false,
    } as unknown as ReturnType<typeof useAcceptPluginManifest>)

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /review change/i }))

    // Modal renders the plugin name in the body copy.
    expect(screen.getByRole('heading', { name: /accept pending manifest change/i })).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /accept manifest change/i }))

    expect(mutateFn).toHaveBeenCalledTimes(1)
    expect(mutateFn).toHaveBeenCalledWith(
      expect.objectContaining({ pluginId: PLUGIN_ID }),
      expect.any(Object),
    )
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
    // The listing instance no longer needs to carry the schema — ConfigTab
    // now derives config_schema from the DETAIL hook. Use a listing instance
    // with config_schema: null to prove the tab is schema-independent of listing.
    mockInstancesLoaded([INSTANCE_WITH_SCHEMA])
    // Supply the Slack-like schema (instance-level) via the detail mock so tests
    // exercise the fixed data flow (ConfigTab reads detail.config_schema).
    mockInstanceDetailLoaded(
      JSON.stringify({ app_level_token: '***', workspace_name: 'Acme' }),
      SLACK_CONFIG_SCHEMA,
    )
    mockMutationNoop()
  })

  it('renders the Config tab with schema fields', () => {
    renderPage()
    // Navigate to Config tab (it may not be active by default when subscription_schema is absent).
    const configTabBtn = screen.getByRole('button', { name: 'Config' })
    fireEvent.click(configTabBtn)
    // workspace_name humanizes to "Workspace Name" in SchemaForm (B1).
    expect(screen.getByLabelText(/workspace name/i)).toBeInTheDocument()
  })

  it('renders secret fields as password inputs', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    // app_level_token humanizes to "App Level Token" in SchemaForm (B1).
    const secretInput = screen.getByLabelText(/app level token/i)
    expect(secretInput).toHaveAttribute('type', 'password')
  })

  it('shows sentinel placeholder for secret fields already set on server', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    // app_level_token humanizes to "App Level Token" in SchemaForm (B1).
    const secretInput = screen.getByLabelText(/app level token/i)
    // Sentinel "***" should be rendered as empty value with a placeholder.
    expect(secretInput).toHaveValue('')
    expect(secretInput).toHaveAttribute('placeholder', '(already set — leave blank to keep)')
  })

  it('renders without crashing for an empty config_schema', () => {
    // Supply an empty schema via the detail mock — the listing schema is irrelevant.
    mockInstanceDetailLoaded('{}', {})
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    expect(screen.getByText(/no config fields declared/i)).toBeInTheDocument()
  })

  it('renders without crashing when config_schema is null', () => {
    // Supply null schema via the detail mock — the listing schema is irrelevant.
    mockInstanceDetailLoaded('{}', null)
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    expect(screen.getByText(/no config fields declared/i)).toBeInTheDocument()
  })

  it('renders schema fields even when the listing instance carries no config_schema', () => {
    // Regression: before the fix, ConfigTab depended on the listing's config_schema
    // (channel schema). Now it reads from the detail response, so a listing instance
    // with config_schema: null must NOT prevent schema fields from rendering.
    mockInstancesLoaded([INSTANCE_NO_SCHEMA])
    mockInstanceDetailLoaded(
      JSON.stringify({ app_level_token: '***' }),
      SLACK_CONFIG_SCHEMA,
    )
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Config' }))
    // app_level_token humanizes to "App Level Token" in SchemaForm (B1).
    const secretInput = screen.getByLabelText(/app level token/i)
    expect(secretInput).toHaveAttribute('type', 'password')
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

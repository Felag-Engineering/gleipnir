import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { CredentialsTab } from './CredentialsTab'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiRedactedCredentials } from '@/api/types'
import { RefreshFailureDetailPrefix } from '@/utils/pluginHealth'

const PLUGIN_ID = 'plug-1'
const INSTANCE_ID = 'inst-1'

const BASE_URL = `/api/v1/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}/credentials`

// Install a fallback GET /credentials handler before every test so that when
// a mutation's onSuccess calls invalidateQueries → re-fetch, the GET returns
// the pre-seeded data rather than failing with a network error.
// This is a no-op for tests that pre-seed the QueryClient and don't trigger
// mutations (those tests never hit the network at all with staleTime: Infinity).
beforeEach(() => {
  server.use(
    http.get(BASE_URL, () => HttpResponse.json({ data: { strategy: 'none' } })),
  )
})

function makeClient(creds?: ApiRedactedCredentials): QueryClient {
  const qc = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        // staleTime: Infinity prevents background refetches from clobbering
        // pre-seeded test data with a failed network call.
        staleTime: Infinity,
      },
      mutations: { retry: false },
    },
  })
  if (creds) {
    qc.setQueryData(queryKeys.plugins.credentials(PLUGIN_ID, INSTANCE_ID), creds)
  }
  return qc
}

function renderTab(
  props: Partial<React.ComponentProps<typeof CredentialsTab>> & {
    creds?: ApiRedactedCredentials
  },
) {
  const { creds, ...rest } = props
  const qc = makeClient(creds)
  return {
    qc,
    ...render(
      <QueryClientProvider client={qc}>
        <CredentialsTab
          pluginId={PLUGIN_ID}
          instanceId={INSTANCE_ID}
          strategy="none"
          canManage={true}
          {...rest}
        />
      </QueryClientProvider>,
    ),
  }
}

// ── strategy: none ────────────────────────────────────────────────────────────

describe('strategy: none', () => {
  it('renders the no-authentication placeholder', () => {
    renderTab({ creds: { strategy: 'none' }, strategy: 'none' })
    expect(screen.getByText(/no authentication credentials/i)).toBeInTheDocument()
  })
})

// ── strategy: static_api_key ──────────────────────────────────────────────────

describe('strategy: static_api_key', () => {
  const creds: ApiRedactedCredentials = {
    strategy: 'static_api_key',
    header_name: 'X-API-Key',
    scheme: 'Bearer',
    has_api_key: true,
  }

  it('renders header name and scheme pre-populated from creds', () => {
    renderTab({ creds, strategy: 'static_api_key' })
    expect(screen.getByDisplayValue('X-API-Key')).toBeInTheDocument()
    expect(screen.getByDisplayValue('Bearer')).toBeInTheDocument()
  })

  it('sends the correct PUT body on save', async () => {
    let captured: unknown
    server.use(
      http.put(BASE_URL + '/static-api-key', async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ data: {} })
      }),
    )

    renderTab({ creds, strategy: 'static_api_key' })

    const apiKeyInput = screen.getByLabelText(/api key/i)
    await userEvent.type(apiKeyInput, 'secret-value')
    await userEvent.click(screen.getByRole('button', { name: /save/i }))

    await waitFor(() => {
      expect(captured).toMatchObject({
        header_name: 'X-API-Key',
        scheme: 'Bearer',
        api_key: 'secret-value',
      })
    })
  })

  it('shows a validation error when api key is empty', async () => {
    renderTab({ creds, strategy: 'static_api_key' })
    await userEvent.click(screen.getByRole('button', { name: /save/i }))
    expect(screen.getByRole('alert')).toHaveTextContent(/api key is required/i)
  })

  it('shows saved confirmation after a successful save', async () => {
    server.use(
      http.put(BASE_URL + '/static-api-key', () =>
        HttpResponse.json({ data: {} }),
      ),
    )
    renderTab({ creds, strategy: 'static_api_key' })
    const apiKeyInput = screen.getByLabelText(/api key/i)
    await userEvent.type(apiKeyInput, 'my-key')
    await userEvent.click(screen.getByRole('button', { name: /save/i }))
    await waitFor(() => expect(screen.getByText('Saved.')).toBeInTheDocument())
  })

  it('auditor: inputs are disabled and save button hidden', () => {
    renderTab({ creds, strategy: 'static_api_key', canManage: false })
    const inputs = screen.getAllByRole('textbox')
    inputs.forEach((input) => expect(input).toBeDisabled())
    expect(screen.queryByRole('button', { name: /save/i })).not.toBeInTheDocument()
  })
})

// ── strategy: header_set ──────────────────────────────────────────────────────

describe('strategy: header_set', () => {
  const creds: ApiRedactedCredentials = {
    strategy: 'header_set',
    header_names: ['X-Auth', 'X-Tenant'],
  }

  it('renders existing header names', () => {
    renderTab({ creds, strategy: 'header_set' })
    expect(screen.getByText('X-Auth')).toBeInTheDocument()
    expect(screen.getByText('X-Tenant')).toBeInTheDocument()
  })

  it('sends DELETE on header delete click', async () => {
    let deletedName = ''
    server.use(
      http.delete(BASE_URL + '/headers/:name', ({ params }) => {
        deletedName = params.name as string
        return HttpResponse.json({ data: {} })
      }),
    )
    renderTab({ creds, strategy: 'header_set' })
    const deleteButtons = screen.getAllByRole('button', { name: /delete/i })
    await userEvent.click(deleteButtons[0])
    await waitFor(() => expect(deletedName).toBe('X-Auth'))
  })

  it('sends PUT on add header with valid name and value', async () => {
    let captured: unknown
    server.use(
      http.put(BASE_URL + '/headers/X-New-Header', async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ data: {} })
      }),
    )
    renderTab({ creds: { strategy: 'header_set', header_names: [] }, strategy: 'header_set' })
    await userEvent.type(screen.getByLabelText(/new header name/i), 'X-New-Header')
    await userEvent.type(screen.getByLabelText(/new header value/i), 'my-value')
    await userEvent.click(screen.getByRole('button', { name: /add/i }))
    await waitFor(() => expect(captured).toEqual({ value: 'my-value' }))
  })

  it('rejects reserved header names client-side', async () => {
    renderTab({ creds: { strategy: 'header_set', header_names: [] }, strategy: 'header_set' })
    await userEvent.type(screen.getByLabelText(/new header name/i), 'Content-Type')
    await userEvent.type(screen.getByLabelText(/new header value/i), 'anything')
    await userEvent.click(screen.getByRole('button', { name: /add/i }))
    expect(screen.getByRole('alert')).toHaveTextContent(/reserved header name/i)
  })

  it('rejects Mcp-Session-Id as a reserved header', async () => {
    renderTab({ creds: { strategy: 'header_set', header_names: [] }, strategy: 'header_set' })
    await userEvent.type(screen.getByLabelText(/new header name/i), 'Mcp-Session-Id')
    await userEvent.type(screen.getByLabelText(/new header value/i), 'anything')
    await userEvent.click(screen.getByRole('button', { name: /add/i }))
    expect(screen.getByRole('alert')).toHaveTextContent(/reserved header name/i)
  })

  it('auditor: add row is hidden', () => {
    renderTab({ creds, strategy: 'header_set', canManage: false })
    expect(screen.queryByLabelText(/new header name/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /add/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /delete/i })).not.toBeInTheDocument()
  })
})

// ── strategy: basic_auth ──────────────────────────────────────────────────────

describe('strategy: basic_auth', () => {
  const creds: ApiRedactedCredentials = {
    strategy: 'basic_auth',
    username: 'admin',
    has_password: true,
  }

  it('renders the pre-populated username', () => {
    renderTab({ creds, strategy: 'basic_auth' })
    expect(screen.getByDisplayValue('admin')).toBeInTheDocument()
  })

  it('sends the correct PUT body on save', async () => {
    let captured: unknown
    server.use(
      http.put(BASE_URL + '/basic-auth', async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ data: {} })
      }),
    )
    renderTab({ creds, strategy: 'basic_auth' })
    const passwordInput = screen.getByLabelText(/password/i)
    await userEvent.type(passwordInput, 'newpass')
    await userEvent.click(screen.getByRole('button', { name: /save/i }))
    await waitFor(() =>
      expect(captured).toMatchObject({ username: 'admin', password: 'newpass' }),
    )
  })

  it('shows a validation error when password is empty', async () => {
    renderTab({ creds, strategy: 'basic_auth' })
    await userEvent.click(screen.getByRole('button', { name: /save/i }))
    expect(screen.getByRole('alert')).toHaveTextContent(/password is required/i)
  })
})

// ── strategy: oauth2_authcode ─────────────────────────────────────────────────

describe('strategy: oauth2_authcode', () => {
  const credsNoToken: ApiRedactedCredentials = {
    strategy: 'oauth2_authcode',
    client_id: 'cid',
    has_client_secret: true,
    authorization_url: 'https://provider.test/authorize',
    token_url: 'https://provider.test/token',
    scopes: ['read'],
    has_token: false,
  }

  const credsWithToken: ApiRedactedCredentials = {
    ...credsNoToken,
    has_token: true,
    token_expires_at: new Date(Date.now() + 3600_000).toISOString(),
  }

  it('shows "Authorize" button when no token', () => {
    renderTab({ creds: credsNoToken, strategy: 'oauth2_authcode' })
    expect(screen.getByRole('button', { name: /^authorize$/i })).toBeInTheDocument()
  })

  it('shows metadata rows (client_id, scopes, token present)', () => {
    renderTab({ creds: credsWithToken, strategy: 'oauth2_authcode', healthState: 'healthy' })
    expect(screen.getByText('cid')).toBeInTheDocument()
    expect(screen.getByText('read')).toBeInTheDocument()
    expect(screen.getByText('Yes')).toBeInTheDocument()
  })

  it('shows "Re-authorize" when token present but refresh failed', () => {
    renderTab({
      creds: credsWithToken,
      strategy: 'oauth2_authcode',
      healthState: 'unhealthy',
      healthDetail: `${RefreshFailureDetailPrefix}: server 401`,
    })
    expect(screen.getByRole('button', { name: /re-authorize/i })).toBeInTheDocument()
  })

  it('shows no oauth button when token present and healthy', () => {
    renderTab({ creds: credsWithToken, strategy: 'oauth2_authcode', healthState: 'healthy' })
    expect(screen.queryByRole('button', { name: /authorize/i })).not.toBeInTheDocument()
  })

  it('auditor: no authorize button even when no token', () => {
    renderTab({ creds: credsNoToken, strategy: 'oauth2_authcode', canManage: false })
    expect(screen.queryByRole('button', { name: /authorize/i })).not.toBeInTheDocument()
  })
})

// ── strategy: oauth2_clientcred ───────────────────────────────────────────────

describe('strategy: oauth2_clientcred', () => {
  const creds: ApiRedactedCredentials = {
    strategy: 'oauth2_clientcred',
    client_id: 'svc',
    has_client_secret: true,
    token_url: 'https://auth.test/token',
    has_token: false,
  }

  it('renders "Authorize" button when no token', () => {
    renderTab({ creds, strategy: 'oauth2_clientcred' })
    expect(screen.getByRole('button', { name: /^authorize$/i })).toBeInTheDocument()
  })
})

// ── strategy mismatch ─────────────────────────────────────────────────────────

describe('strategy mismatch', () => {
  it('shows a mismatch warning when server strategy differs from prop strategy', () => {
    renderTab({
      creds: { strategy: 'basic_auth', username: 'admin' },
      strategy: 'static_api_key',
    })
    expect(screen.getByRole('alert')).toHaveTextContent(/strategy mismatch/i)
  })
})

// ── error state ───────────────────────────────────────────────────────────────

describe('error state', () => {
  it('shows error message when credentials fetch fails', async () => {
    // No pre-seeded data; server returns 500 so useQuery transitions to error.
    server.use(
      http.get(BASE_URL, () =>
        HttpResponse.json({ error: 'Internal error' }, { status: 500 }),
      ),
    )
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <CredentialsTab
          pluginId={PLUGIN_ID}
          instanceId={INSTANCE_ID}
          strategy="static_api_key"
          canManage
        />
      </QueryClientProvider>,
    )
    await waitFor(() =>
      expect(screen.getByText(/could not load credentials/i)).toBeInTheDocument(),
    )
  })
})

// ── clear credentials ─────────────────────────────────────────────────────────

describe('clear credentials', () => {
  it('shows Clear button when something is set and canManage=true', () => {
    renderTab({
      creds: { strategy: 'static_api_key', has_api_key: true },
      strategy: 'static_api_key',
    })
    expect(screen.getByRole('button', { name: /clear credentials/i })).toBeInTheDocument()
  })

  it('calls DELETE /credentials on clear', async () => {
    let called = false
    server.use(
      http.delete(BASE_URL, () => {
        called = true
        return HttpResponse.json({ data: {} })
      }),
    )
    renderTab({
      creds: { strategy: 'static_api_key', has_api_key: true },
      strategy: 'static_api_key',
    })
    await userEvent.click(screen.getByRole('button', { name: /clear credentials/i }))
    await waitFor(() => expect(called).toBe(true))
  })

  it('auditor: Clear button is hidden', () => {
    renderTab({
      creds: { strategy: 'static_api_key', has_api_key: true },
      strategy: 'static_api_key',
      canManage: false,
    })
    expect(screen.queryByRole('button', { name: /clear credentials/i })).not.toBeInTheDocument()
  })

  it('hides Clear button when nothing is set', () => {
    renderTab({
      creds: { strategy: 'static_api_key', has_api_key: false },
      strategy: 'static_api_key',
    })
    expect(screen.queryByRole('button', { name: /clear credentials/i })).not.toBeInTheDocument()
  })
})

// ── API error surfacing ───────────────────────────────────────────────────────

describe('mutation error surfacing', () => {
  it('shows inline error when PUT static-api-key fails', async () => {
    server.use(
      http.put(BASE_URL + '/static-api-key', () =>
        HttpResponse.json({ error: 'Strategy mismatch', detail: 'Cannot set static_api_key on a basic_auth instance.' }, { status: 400 }),
      ),
    )
    renderTab({
      creds: { strategy: 'static_api_key', header_name: 'X-Key', has_api_key: false },
      strategy: 'static_api_key',
    })
    // header_name is already populated; just fill api_key
    await userEvent.type(screen.getByLabelText(/api key/i), 'bad-key')
    await userEvent.click(screen.getByRole('button', { name: /save/i }))
    // errMessage returns the detail field first, then falls back to error
    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(/cannot set static_api_key/i)
    })
  })

  it('shows inline error when PUT header fails', async () => {
    server.use(
      http.put(BASE_URL + '/headers/:name', () =>
        HttpResponse.json({ error: 'Reserved header', detail: 'Mcp-Session-Id is reserved.' }, { status: 400 }),
      ),
    )
    renderTab({ creds: { strategy: 'header_set', header_names: [] }, strategy: 'header_set' })
    await userEvent.type(screen.getByLabelText(/new header name/i), 'X-Fine')
    await userEvent.type(screen.getByLabelText(/new header value/i), 'val')
    await userEvent.click(screen.getByRole('button', { name: /add/i }))
    // errMessage returns the detail field first, then falls back to error
    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(/mcp-session-id is reserved/i)
    })
  })
})

// ── mock vi.fn for window.location.href ──────────────────────────────────────

// The Authorize button mutates window.location.href on success. We don't need
// to assert against that here (ReauthorizeButton tests cover it); just verify
// the mutation is triggered.
describe('authorize button calls oauth/begin', () => {
  it('clicks Authorize and posts to oauth/begin', async () => {
    let called = false
    server.use(
      http.post(
        `/api/v1/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}/oauth/begin`,
        () => {
          called = true
          // Return without authorize_url → clientcred-like path (no redirect)
          return HttpResponse.json({ data: { status: 'ok' } })
        },
      ),
    )
    // Also need pluginInstances invalidation path — set a no-op handler
    renderTab({
      creds: { strategy: 'oauth2_authcode', has_token: false, client_id: 'c', token_url: 't' },
      strategy: 'oauth2_authcode',
    })
    await userEvent.click(screen.getByRole('button', { name: /^authorize$/i }))
    await waitFor(() => expect(called).toBe(true))
  })

  it('shows inline error banner when oauth/begin returns 400', async () => {
    server.use(
      http.post(
        `/api/v1/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}/oauth/begin`,
        () =>
          HttpResponse.json(
            {
              error: 'oauth configuration invalid',
              detail:
                'oauth begin: public_url is not configured; set it in admin settings before starting an OAuth flow',
            },
            { status: 400 },
          ),
      ),
    )
    renderTab({
      creds: { strategy: 'oauth2_authcode', has_token: false, client_id: 'c', token_url: 't' },
      strategy: 'oauth2_authcode',
    })
    await userEvent.click(screen.getByRole('button', { name: /^authorize$/i }))
    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(
        /public_url is not configured/i,
      )
    })
  })
})

// Note: tests that assert window.location.href (e.g. authorize button test) do
// not need a stub here because the mock server returns status:'ok' without
// authorize_url, so ReauthorizeButton.onSuccess skips the href assignment.

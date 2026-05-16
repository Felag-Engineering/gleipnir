import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { CredentialsTab } from './CredentialsTab'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiRedactedCredentials } from '@/api/types'
import { RefreshFailureDetailPrefix } from '@/utils/pluginHealth'

const PLUGIN_ID = 'plugin-test-01'
const INSTANCE_ID = 'inst-test-prod'

const CREDS_URL = `/api/v1/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}/credentials`

function makeQueryClient(creds: ApiRedactedCredentials): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.plugins.credentials(PLUGIN_ID, INSTANCE_ID), creds)
  return qc
}

function Wrapper({
  creds,
  children,
}: {
  creds: ApiRedactedCredentials
  children: React.ReactNode
}) {
  return (
    <QueryClientProvider client={makeQueryClient(creds)}>
      <div style={{ maxWidth: 640, padding: 24 }}>{children}</div>
    </QueryClientProvider>
  )
}

const meta: Meta<typeof CredentialsTab> = {
  title: 'Admin/CredentialsTab',
  component: CredentialsTab,
}

export default meta
type Story = StoryObj<typeof CredentialsTab>

// ── none ──────────────────────────────────────────────────────────────────────

export const None: Story = {
  render: () => (
    <Wrapper creds={{ strategy: 'none' }}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="none"
        canManage
      />
    </Wrapper>
  ),
}

// ── static_api_key ────────────────────────────────────────────────────────────

const STATIC_API_KEY_CREDS: ApiRedactedCredentials = {
  strategy: 'static_api_key',
  header_name: 'X-API-Key',
  scheme: 'Bearer',
  has_api_key: true,
}

export const StaticAPIKey: Story = {
  parameters: {
    msw: {
      handlers: [
        http.put(CREDS_URL + '/static-api-key', () =>
          HttpResponse.json({ data: {} }),
        ),
      ],
    },
  },
  render: () => (
    <Wrapper creds={STATIC_API_KEY_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="static_api_key"
        canManage
      />
    </Wrapper>
  ),
}

export const StaticAPIKeyAuditor: Story = {
  render: () => (
    <Wrapper creds={STATIC_API_KEY_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="static_api_key"
        canManage={false}
      />
    </Wrapper>
  ),
}

// ── header_set ────────────────────────────────────────────────────────────────

const HEADER_SET_CREDS: ApiRedactedCredentials = {
  strategy: 'header_set',
  header_names: ['X-Custom-Auth', 'X-Tenant-ID'],
}

export const HeaderSet: Story = {
  parameters: {
    msw: {
      handlers: [
        http.put(CREDS_URL + '/headers/:name', () =>
          HttpResponse.json({ data: {} }),
        ),
        http.delete(CREDS_URL + '/headers/:name', () =>
          HttpResponse.json({ data: {} }),
        ),
      ],
    },
  },
  render: () => (
    <Wrapper creds={HEADER_SET_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="header_set"
        canManage
      />
    </Wrapper>
  ),
}

export const HeaderSetEmpty: Story = {
  parameters: {
    msw: {
      handlers: [
        http.put(CREDS_URL + '/headers/:name', () =>
          HttpResponse.json({ data: {} }),
        ),
      ],
    },
  },
  render: () => (
    <Wrapper creds={{ strategy: 'header_set', header_names: [] }}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="header_set"
        canManage
      />
    </Wrapper>
  ),
}

export const HeaderSetAuditor: Story = {
  render: () => (
    <Wrapper creds={HEADER_SET_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="header_set"
        canManage={false}
      />
    </Wrapper>
  ),
}

// ── basic_auth ────────────────────────────────────────────────────────────────

const BASIC_AUTH_CREDS: ApiRedactedCredentials = {
  strategy: 'basic_auth',
  username: 'service-account',
  has_password: true,
}

export const BasicAuth: Story = {
  parameters: {
    msw: {
      handlers: [
        http.put(CREDS_URL + '/basic-auth', () =>
          HttpResponse.json({ data: {} }),
        ),
      ],
    },
  },
  render: () => (
    <Wrapper creds={BASIC_AUTH_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="basic_auth"
        canManage
      />
    </Wrapper>
  ),
}

// ── oauth2_authcode — no token yet ────────────────────────────────────────────

const OAUTH_NO_TOKEN_CREDS: ApiRedactedCredentials = {
  strategy: 'oauth2_authcode',
  client_id: 'my-client-id',
  has_client_secret: true,
  authorization_url: 'https://provider.example.com/authorize',
  token_url: 'https://provider.example.com/token',
  scopes: ['read', 'write'],
  has_token: false,
}

export const OAuth2AuthcodeNoToken: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post(
          `/api/v1/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}/oauth/begin`,
          () =>
            HttpResponse.json({
              data: { authorize_url: 'https://provider.example.com/authorize?state=xyz' },
            }),
        ),
      ],
    },
  },
  render: () => (
    <Wrapper creds={OAUTH_NO_TOKEN_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="oauth2_authcode"
        canManage
      />
    </Wrapper>
  ),
}

// ── oauth2_authcode — token present, healthy ──────────────────────────────────

const OAUTH_WITH_TOKEN_CREDS: ApiRedactedCredentials = {
  strategy: 'oauth2_authcode',
  client_id: 'my-client-id',
  has_client_secret: true,
  authorization_url: 'https://provider.example.com/authorize',
  token_url: 'https://provider.example.com/token',
  scopes: ['read', 'write'],
  has_token: true,
  token_expires_at: new Date(Date.now() + 3600 * 1000).toISOString(),
}

export const OAuth2AuthcodeHealthy: Story = {
  render: () => (
    <Wrapper creds={OAUTH_WITH_TOKEN_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="oauth2_authcode"
        healthState="healthy"
        canManage
      />
    </Wrapper>
  ),
}

// ── oauth2_authcode — refresh failed (Re-authorize CTA) ──────────────────────

export const OAuth2AuthcodeRefreshFailed: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post(
          `/api/v1/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}/oauth/begin`,
          () =>
            HttpResponse.json({
              data: { authorize_url: 'https://provider.example.com/authorize?state=xyz' },
            }),
        ),
      ],
    },
  },
  render: () => (
    <Wrapper creds={OAUTH_WITH_TOKEN_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="oauth2_authcode"
        healthState="unhealthy"
        healthDetail={`${RefreshFailureDetailPrefix}: token expired`}
        canManage
      />
    </Wrapper>
  ),
}

// ── oauth2_clientcred ─────────────────────────────────────────────────────────

const OAUTH_CLIENTCRED_CREDS: ApiRedactedCredentials = {
  strategy: 'oauth2_clientcred',
  client_id: 'service-app',
  has_client_secret: true,
  token_url: 'https://auth.example.com/token',
  scopes: ['api.read'],
  has_token: true,
}

export const OAuth2Clientcred: Story = {
  render: () => (
    <Wrapper creds={OAUTH_CLIENTCRED_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="oauth2_clientcred"
        healthState="healthy"
        canManage
      />
    </Wrapper>
  ),
}

// ── Auditor read-only (OAuth) ─────────────────────────────────────────────────

export const AuditorReadOnly: Story = {
  render: () => (
    <Wrapper creds={OAUTH_WITH_TOKEN_CREDS}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="oauth2_authcode"
        healthState="healthy"
        canManage={false}
      />
    </Wrapper>
  ),
}

// ── Strategy mismatch warning ─────────────────────────────────────────────────

// Server returns basic_auth, but manifest now declares static_api_key.
// This can happen briefly during a hot-reload before the credential row is migrated.
export const StrategyMismatch: Story = {
  render: () => (
    <Wrapper creds={{ strategy: 'basic_auth', username: 'admin', has_password: true }}>
      <CredentialsTab
        pluginId={PLUGIN_ID}
        instanceId={INSTANCE_ID}
        strategy="static_api_key"
        canManage
      />
    </Wrapper>
  ),
}

import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import AdminPluginInstancePage from './AdminPluginInstancePage'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiPluginInstanceForAudience, ApiRedactedCredentials } from '@/api/types'
import { RefreshFailureDetailPrefix } from '@/utils/pluginHealth'

const PLUGIN_ID = 'plugin-slack-01'
const INSTANCE_ID = 'inst-slack-prod'

const FIXTURE_WITH_SCHEMA: ApiPluginInstanceForAudience = {
  id: INSTANCE_ID,
  plugin_id: PLUGIN_ID,
  plugin_name: 'Slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  auth_strategy: 'oauth2_authcode',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  version: 2,
  event_kinds: [
    {
      kind: 'channel_message',
      description: 'A message posted in a channel',
    },
  ],
  subscription_schema: {
    type: 'object',
    additionalProperties: false,
    required: ['channels'],
    properties: {
      channels: {
        type: 'array',
        items: { type: 'string', pattern: '^C[A-Z0-9]+$' },
        description: 'Slack channel IDs to watch (e.g. C012ABCDEF)',
      },
    },
  },
  subscription_scope: { channels: ['C012ABCDEF', 'C09OPSCH'] },
}

const FIXTURE_NO_SCHEMA: ApiPluginInstanceForAudience = {
  id: INSTANCE_ID,
  plugin_id: PLUGIN_ID,
  plugin_name: 'Webhook',
  instance_name: 'webhook-prod',
  state: 'healthy',
  implements_notify: false,
  implements_request: false,
  config_schema: null,
  version: 0,
  event_kinds: [],
  auth_strategy: 'none',
}

const FIXTURE_OAUTH_REFRESH_FAILED_AUTHCODE: ApiPluginInstanceForAudience = {
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
  version: 3,
  event_kinds: [],
  subscription_schema: {
    type: 'object',
    properties: {
      channels: { type: 'array', items: { type: 'string' } },
    },
  },
  subscription_scope: { channels: ['C012ABCDEF'] },
}

const FIXTURE_OAUTH_REFRESH_FAILED_CLIENTCRED: ApiPluginInstanceForAudience = {
  id: INSTANCE_ID,
  plugin_id: PLUGIN_ID,
  plugin_name: 'Jira',
  instance_name: 'jira-prod',
  state: 'unhealthy',
  health_detail: `${RefreshFailureDetailPrefix}: server returned 401`,
  auth_strategy: 'oauth2_clientcred',
  implements_notify: false,
  implements_request: true,
  config_schema: null,
  version: 5,
  event_kinds: [],
}

// Default redacted credentials fixture — used by MSW to respond to GET /credentials.
// Seeded so the CredentialsTab shows meaningful content in stories.
const FIXTURE_CREDENTIALS_OAUTH: ApiRedactedCredentials = {
  strategy: 'oauth2_authcode',
  client_id: 'story-client-id',
  has_client_secret: true,
  authorization_url: 'https://provider.example.com/authorize',
  token_url: 'https://provider.example.com/token',
  scopes: ['read', 'write'],
  has_token: true,
  token_expires_at: new Date(Date.now() + 3600 * 1000).toISOString(),
}

const FIXTURE_CREDENTIALS_NONE: ApiRedactedCredentials = { strategy: 'none' }

// MSW handlers for the credentials endpoint so the CredentialsTab renders in
// all existing stories without needing a running backend.
const CREDENTIALS_HANDLERS = [
  http.get(
    `/api/v1/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}/credentials`,
    () => HttpResponse.json({ data: FIXTURE_CREDENTIALS_OAUTH }),
  ),
]

const CREDENTIALS_HANDLERS_NONE = [
  http.get(
    `/api/v1/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}/credentials`,
    () => HttpResponse.json({ data: FIXTURE_CREDENTIALS_NONE }),
  ),
]

// currentUser stub — stories render as admin so write controls are shown.
const CURRENT_USER_HANDLER = http.get(
  '/api/v1/auth/me',
  () => HttpResponse.json({ data: { id: 'u1', username: 'admin', roles: ['admin'] } }),
)

function makeQueryClient(instances: ApiPluginInstanceForAudience[]): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.admin.pluginInstances, instances)
  return qc
}

function Wrapper({ instances }: { instances: ApiPluginInstanceForAudience[] }) {
  return (
    <QueryClientProvider client={makeQueryClient(instances)}>
      <MemoryRouter
        initialEntries={[`/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}`]}
      >
        <Routes>
          <Route
            path="/admin/plugins/:id/instances/:iid"
            element={<AdminPluginInstancePage />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

const meta: Meta<typeof AdminPluginInstancePage> = {
  title: 'Admin/PluginInstancePage',
  component: AdminPluginInstancePage,
}

export default meta
type Story = StoryObj<typeof AdminPluginInstancePage>

// Renders the Subscriptions tab because the fixture has subscription_schema.
export const WithSubscriptionSchema: Story = {
  parameters: {
    msw: { handlers: [...CREDENTIALS_HANDLERS, CURRENT_USER_HANDLER] },
  },
  render: () => <Wrapper instances={[FIXTURE_WITH_SCHEMA]} />,
}

// No subscription_schema → Subscriptions tab is hidden; defaults to Config tab.
export const NoSubscriptionSchema: Story = {
  parameters: {
    msw: { handlers: [...CREDENTIALS_HANDLERS_NONE, CURRENT_USER_HANDLER] },
  },
  render: () => <Wrapper instances={[FIXTURE_NO_SCHEMA]} />,
}

// Loading state — no query data seeded; the page shows the loading message.
export const Loading: Story = {
  render: () => (
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter
        initialEntries={[`/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}`]}
      >
        <Routes>
          <Route
            path="/admin/plugins/:id/instances/:iid"
            element={<AdminPluginInstancePage />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  ),
}

// OAuth refresh failed (authcode) — shows the Re-authorize banner with an
// oauth2_authcode strategy instance. Clicking Re-authorize would navigate
// to the provider's authorization page.
export const OAuthRefreshFailedAuthcode: Story = {
  parameters: {
    msw: { handlers: [...CREDENTIALS_HANDLERS, CURRENT_USER_HANDLER] },
  },
  render: () => <Wrapper instances={[FIXTURE_OAUTH_REFRESH_FAILED_AUTHCODE]} />,
}

// OAuth refresh failed (clientcred) — same banner, but Re-authorize performs
// the client credentials exchange synchronously (no browser redirect).
export const OAuthRefreshFailedClientcred: Story = {
  parameters: {
    msw: { handlers: [...CREDENTIALS_HANDLERS, CURRENT_USER_HANDLER] },
  },
  render: () => <Wrapper instances={[FIXTURE_OAUTH_REFRESH_FAILED_CLIENTCRED]} />,
}

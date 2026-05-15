import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import '@/tokens.css'
import AdminPluginsPage from './AdminPluginsPage'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiPluginInstanceForAudience } from '@/api/types'

const CALLBACK_URL = 'https://gleipnir.example.com/api/v1/admin/plugins/oauth/callback'
const OLD_CALLBACK_URL = 'https://old.example.com/api/v1/admin/plugins/oauth/callback'

const INSTANCE_HEALTHY: ApiPluginInstanceForAudience = {
  id: 'inst-slack-prod',
  plugin_id: 'plugin-slack-01',
  plugin_name: 'Slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  auth_strategy: 'oauth2_authcode',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  version: 2,
  event_kinds: [],
  last_oauth_callback_url: CALLBACK_URL,
}

const INSTANCE_PENDING_REAUTH: ApiPluginInstanceForAudience = {
  id: 'inst-jira-prod',
  plugin_id: 'plugin-jira-01',
  plugin_name: 'Jira',
  instance_name: 'jira-prod',
  state: 'pending_reauthorize',
  auth_strategy: 'oauth2_authcode',
  implements_notify: false,
  implements_request: true,
  config_schema: null,
  version: 3,
  event_kinds: [],
  last_oauth_callback_url: OLD_CALLBACK_URL,
  health_detail: `public_url changed: recorded callback "${OLD_CALLBACK_URL}" no longer matches current "${CALLBACK_URL}"; re-authorize to update`,
}

const INSTANCE_UNHEALTHY: ApiPluginInstanceForAudience = {
  id: 'inst-github-prod',
  plugin_id: 'plugin-github-01',
  plugin_name: 'GitHub',
  instance_name: 'github-prod',
  state: 'unhealthy',
  auth_strategy: 'oauth2_clientcred',
  implements_notify: true,
  implements_request: false,
  config_schema: null,
  version: 1,
  event_kinds: [],
}

function makeQueryClient(instances: ApiPluginInstanceForAudience[]): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.admin.pluginInstances, instances)
  return qc
}

function Wrapper({ instances }: { instances: ApiPluginInstanceForAudience[] }) {
  return (
    <QueryClientProvider client={makeQueryClient(instances)}>
      <MemoryRouter initialEntries={['/admin/plugins']}>
        <Routes>
          <Route path="/admin/plugins" element={<AdminPluginsPage />} />
          <Route
            path="/admin/plugins/:id/instances/:iid"
            element={<div>Instance detail page</div>}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

const meta: Meta<typeof AdminPluginsPage> = {
  title: 'Admin/PluginsPage',
  component: AdminPluginsPage,
}

export default meta
type Story = StoryObj<typeof AdminPluginsPage>

// All instances healthy — no "Needs re-authorization" section.
export const AllHealthy: Story = {
  render: () => <Wrapper instances={[INSTANCE_HEALTHY]} />,
}

// One instance pending re-authorization — shows the "Needs re-authorization"
// section at the top, grouped separately from the "All instances" list.
export const WithPendingReauth: Story = {
  render: () => (
    <Wrapper instances={[INSTANCE_PENDING_REAUTH, INSTANCE_HEALTHY, INSTANCE_UNHEALTHY]} />
  ),
}

// Empty — no plugin instances installed.
export const Empty: Story = {
  render: () => <Wrapper instances={[]} />,
}

// Loading — query data not yet seeded; page shows the loading skeleton.
export const Loading: Story = {
  render: () => (
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={['/admin/plugins']}>
        <Routes>
          <Route path="/admin/plugins" element={<AdminPluginsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  ),
}

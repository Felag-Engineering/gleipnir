import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import AdminPluginsPage from './AdminPluginsPage'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiPluginInstanceForAudience, ApiPluginRSS } from '@/api/types'

// Fixture RSS data for stories that want to demonstrate the memory bar.
const RSS_WITH_INSTANCES: ApiPluginRSS = {
  total_bytes: 431915008, // ≈ 412 MB
  instance_count: 3,
  instances: [
    {
      instance_id: 'inst-slack-prod',
      instance_name: 'slack-prod',
      plugin_id: 'plugin-slack-01',
      rss_bytes: 209715200, // 200 MB
      sampled_at: new Date().toISOString(),
    },
    {
      instance_id: 'inst-slack-staging',
      instance_name: 'slack-staging',
      plugin_id: 'plugin-slack-01',
      rss_bytes: 157286400, // 150 MB
      sampled_at: new Date().toISOString(),
    },
    {
      instance_id: 'inst-github-prod',
      instance_name: 'github-prod',
      plugin_id: 'plugin-github-01',
      rss_bytes: 64913408, // ≈ 61.9 MB
      sampled_at: new Date().toISOString(),
    },
  ],
}

const RSS_EMPTY: ApiPluginRSS = {
  total_bytes: 0,
  instance_count: 0,
  instances: [],
}

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
  plugin_version: '1.2.0',
  services: ['channel', 'trigger'],
}

// Second Slack instance on the same plugin — demonstrates instance-count > 1 on the card.
const INSTANCE_HEALTHY_SLACK_STAGING: ApiPluginInstanceForAudience = {
  id: 'inst-slack-staging',
  plugin_id: 'plugin-slack-01',
  plugin_name: 'Slack',
  instance_name: 'slack-staging',
  state: 'healthy',
  auth_strategy: 'oauth2_authcode',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  version: 1,
  event_kinds: [],
  plugin_version: '1.2.0',
  services: ['channel', 'trigger'],
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
  plugin_version: '2.0.1',
  services: ['tool'],
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
  plugin_version: '0.9.0',
  services: ['tool', 'channel'],
}

function makeQueryClient(
  instances: ApiPluginInstanceForAudience[],
  currentUser?: { id: string; username: string; roles: string[] },
  rss?: ApiPluginRSS,
): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.admin.pluginInstances, instances)
  if (currentUser) {
    qc.setQueryData(queryKeys.currentUser.all, currentUser)
  }
  if (rss !== undefined) {
    qc.setQueryData(queryKeys.plugins.rss(), rss)
  }
  return qc
}

const ADMIN_USER = { id: 'user-01', username: 'admin', roles: ['admin'] }
const AUDITOR_USER = { id: 'user-02', username: 'auditor', roles: ['auditor'] }

function Wrapper({
  instances,
  currentUser,
  rss,
}: {
  instances: ApiPluginInstanceForAudience[]
  currentUser?: { id: string; username: string; roles: string[] }
  rss?: ApiPluginRSS
}) {
  return (
    <QueryClientProvider client={makeQueryClient(instances, currentUser, rss)}>
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

// All instances healthy — no "Needs re-authorization" section (non-admin user).
export const AllHealthy: Story = {
  render: () => <Wrapper instances={[INSTANCE_HEALTHY]} />,
}

// Admin view with two plugins and two Slack instances — shows the two-pane layout
// with the left pane having cards (Slack with 2 instances, GitHub with 1), and the
// right pane showing the auto-selected first plugin's instance table. The memory
// bar shows "≈ 412 MB across 3 instances" in the page header.
export const Admin: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/plugins/rss', () =>
          HttpResponse.json({ data: RSS_WITH_INSTANCES }),
        ),
        http.post('/api/v1/admin/plugins', () =>
          HttpResponse.json({
            data: { id: 'plugin-new', name: 'NewPlugin', version: '1.0.0', status: 'active' },
          }),
        ),
        http.post('/api/v1/admin/plugins/:id/instances', () =>
          HttpResponse.json({
            data: {
              id: 'inst-new',
              plugin_id: 'plugin-new',
              instance_name: 'production',
              health_state: 'healthy',
              version: 1,
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          }),
        ),
      ],
    },
  },
  render: () => (
    <Wrapper
      instances={[INSTANCE_HEALTHY, INSTANCE_HEALTHY_SLACK_STAGING, INSTANCE_UNHEALTHY]}
      currentUser={ADMIN_USER}
      rss={RSS_WITH_INSTANCES}
    />
  ),
}

// TwoPluginsSelected — two plugins visible, demonstrating how clicking between
// cards updates the right-pane detail view.
export const TwoPluginsSelected: Story = {
  render: () => (
    <Wrapper
      instances={[INSTANCE_HEALTHY, INSTANCE_UNHEALTHY]}
      currentUser={ADMIN_USER}
    />
  ),
}

// Auditor view — Install plugin button is hidden; read-only view.
export const Auditor: Story = {
  render: () => (
    <Wrapper instances={[INSTANCE_HEALTHY, INSTANCE_UNHEALTHY]} currentUser={AUDITOR_USER} />
  ),
}

// AdminEmpty — admin with zero instances; Install button still visible above the empty state.
export const AdminEmpty: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins', () =>
          HttpResponse.json({
            data: { id: 'plugin-new', name: 'NewPlugin', version: '1.0.0', status: 'active' },
          }),
        ),
      ],
    },
  },
  render: () => <Wrapper instances={[]} currentUser={ADMIN_USER} />,
}

// One instance pending re-authorization — shows the "Needs re-authorization"
// section at the top above the two-pane layout.
export const WithPendingReauth: Story = {
  render: () => (
    <Wrapper instances={[INSTANCE_PENDING_REAUTH, INSTANCE_HEALTHY, INSTANCE_UNHEALTHY]} currentUser={ADMIN_USER} />
  ),
}

// InstallDisabled503 — clicking Install shows the 503 amber disabled notice.
export const InstallDisabled503: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins', () =>
          HttpResponse.json({ error: 'plugin system disabled' }, { status: 503 }),
        ),
      ],
    },
  },
  render: () => <Wrapper instances={[]} currentUser={ADMIN_USER} />,
}

// Empty — no plugin instances installed (non-admin).
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

// NoRunningInstances — RSS response returns 0 instances; memory bar is hidden.
// Demonstrates that the memory bar does not appear when no plugin subprocesses
// are running (e.g. plugins are installed but all stopped or crashed).
export const NoRunningInstances: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/plugins/rss', () =>
          HttpResponse.json({ data: RSS_EMPTY }),
        ),
      ],
    },
  },
  render: () => (
    <Wrapper
      instances={[INSTANCE_HEALTHY]}
      currentUser={ADMIN_USER}
      rss={RSS_EMPTY}
    />
  ),
}

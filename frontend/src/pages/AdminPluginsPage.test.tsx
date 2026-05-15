import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

import AdminPluginsPage from './AdminPluginsPage'
import type { ApiPluginInstanceForAudience } from '@/api/types'

// --- Module mocks ---

vi.mock('@/hooks/queries/admin')
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'

// --- Fixtures ---

const CALLBACK_URL = 'https://gleipnir.example.com/api/v1/admin/plugins/oauth/callback'
const OLD_CALLBACK_URL = 'https://old.example.com/api/v1/admin/plugins/oauth/callback'

const PLUGIN_ID = 'plugin-slack-01'
const INSTANCE_ID_SLACK = 'inst-slack-prod'
const INSTANCE_ID_JIRA = 'inst-jira-prod'

const INSTANCE_HEALTHY: ApiPluginInstanceForAudience = {
  id: INSTANCE_ID_SLACK,
  plugin_id: PLUGIN_ID,
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
  id: INSTANCE_ID_JIRA,
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
}

// --- Helpers ---

function mockInstances(instances: ApiPluginInstanceForAudience[]) {
  vi.mocked(usePluginInstancesForAudience).mockReturnValue({
    data: instances,
    status: 'success',
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof usePluginInstancesForAudience>)
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/admin/plugins']}>
        <Routes>
          <Route path="/admin/plugins" element={<AdminPluginsPage />} />
          <Route
            path="/admin/plugins/:id/instances/:iid"
            element={<div>Instance detail</div>}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// --- Tests ---

describe('AdminPluginsPage', () => {
  it('shows "Needs re-authorization" section when at least one instance is pending_reauthorize', () => {
    mockInstances([INSTANCE_PENDING_REAUTH, INSTANCE_HEALTHY])

    renderPage()

    expect(screen.getByText('Needs re-authorization')).toBeInTheDocument()
    // The Jira instance should appear somewhere on the page (both sections may
    // render its name, so use getAllByText and assert at least one match).
    const jiraLinks = screen.getAllByText('jira-prod')
    expect(jiraLinks.length).toBeGreaterThanOrEqual(1)
  })

  it('does not show "Needs re-authorization" section when no instances are pending', () => {
    mockInstances([INSTANCE_HEALTHY])

    renderPage()

    expect(screen.queryByText('Needs re-authorization')).not.toBeInTheDocument()
  })

  it('shows all instances in the "All instances" section regardless of state', () => {
    mockInstances([INSTANCE_PENDING_REAUTH, INSTANCE_HEALTHY])

    renderPage()

    expect(screen.getByText('All instances')).toBeInTheDocument()
    // Both instances appear in the All instances section.
    const slackLinks = screen.getAllByText('slack-prod')
    expect(slackLinks.length).toBeGreaterThanOrEqual(1)
    const jiraLinks = screen.getAllByText('jira-prod')
    expect(jiraLinks.length).toBeGreaterThanOrEqual(1)
  })

  it('instance row links point to /admin/plugins/{plugin_id}/instances/{id}', () => {
    mockInstances([INSTANCE_PENDING_REAUTH])

    renderPage()

    const links = screen.getAllByRole('link', { name: 'jira-prod' })
    for (const link of links) {
      expect(link).toHaveAttribute(
        'href',
        `/admin/plugins/${encodeURIComponent(INSTANCE_PENDING_REAUTH.plugin_id)}/instances/${encodeURIComponent(INSTANCE_ID_JIRA)}`,
      )
    }
  })

  it('shows empty state when no instances are installed', () => {
    mockInstances([])

    renderPage()

    expect(screen.getByText('No plugin instances')).toBeInTheDocument()
  })
})

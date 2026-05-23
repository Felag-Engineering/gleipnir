import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, act, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'

import AdminPluginsPage from './AdminPluginsPage'
import type { ApiPluginInstanceForAudience } from '@/api/types'

// --- Module mocks ---

vi.mock('@/hooks/queries/admin')
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'

vi.mock('@/hooks/queries/users')
import { useCurrentUser } from '@/hooks/queries/users'

// --- Fixtures ---

const CALLBACK_URL = 'https://gleipnir.example.com/api/v1/admin/plugins/oauth/callback'
const OLD_CALLBACK_URL = 'https://old.example.com/api/v1/admin/plugins/oauth/callback'

const PLUGIN_ID = 'plugin-slack-01'
const PLUGIN_ID_JIRA = 'plugin-jira-01'
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
  plugin_id: PLUGIN_ID_JIRA,
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

function mockCurrentUser(roles: string[]) {
  vi.mocked(useCurrentUser).mockReturnValue({
    data: { id: 'user-01', username: 'admin', roles },
    status: 'success',
    isLoading: false,
    isError: false,
  } as unknown as ReturnType<typeof useCurrentUser>)
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
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

// Ensure real timers are restored after any test that switches to fake timers.
afterEach(() => {
  vi.useRealTimers()
})

// --- Tests ---

describe('AdminPluginsPage', () => {
  it('shows "Needs re-authorization" section when at least one instance is pending_reauthorize', () => {
    mockInstances([INSTANCE_PENDING_REAUTH, INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    renderPage()

    expect(screen.getByText('Needs re-authorization')).toBeInTheDocument()
    const jiraLinks = screen.getAllByText('jira-prod')
    expect(jiraLinks.length).toBeGreaterThanOrEqual(1)
  })

  it('does not show "Needs re-authorization" section when no instances are pending', () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    renderPage()

    expect(screen.queryByText('Needs re-authorization')).not.toBeInTheDocument()
  })

  it('shows all instances in the "All instances" section regardless of state', () => {
    mockInstances([INSTANCE_PENDING_REAUTH, INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    renderPage()

    expect(screen.getByText('All instances')).toBeInTheDocument()
    const slackLinks = screen.getAllByText('slack-prod')
    expect(slackLinks.length).toBeGreaterThanOrEqual(1)
    const jiraLinks = screen.getAllByText('jira-prod')
    expect(jiraLinks.length).toBeGreaterThanOrEqual(1)
  })

  it('instance row links point to /admin/plugins/{plugin_id}/instances/{id}', () => {
    mockInstances([INSTANCE_PENDING_REAUTH])
    mockCurrentUser(['admin'])
    renderPage()

    const links = screen.getAllByRole('link', { name: 'jira-prod' })
    for (const link of links) {
      expect(link).toHaveAttribute(
        'href',
        `/admin/plugins/${encodeURIComponent(PLUGIN_ID_JIRA)}/instances/${encodeURIComponent(INSTANCE_ID_JIRA)}`,
      )
    }
  })

  it('shows empty state when no instances are installed', () => {
    mockInstances([])
    mockCurrentUser(['admin'])
    renderPage()

    expect(screen.getByText('No plugin instances')).toBeInTheDocument()
  })

  // --- Role gating ---

  it('renders Install plugin button for admin role', () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    renderPage()

    // Use exact name to distinguish from "Uninstall plugin" in the kebab menu.
    expect(screen.getByRole('button', { name: 'Install plugin' })).toBeInTheDocument()
  })

  it('does NOT render Install plugin button for auditor role', () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['auditor'])
    renderPage()

    expect(screen.queryByRole('button', { name: 'Install plugin' })).not.toBeInTheDocument()
  })

  it('does NOT render Install plugin button for operator role', () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['operator'])
    renderPage()

    expect(screen.queryByRole('button', { name: 'Install plugin' })).not.toBeInTheDocument()
  })

  it('Install plugin button is visible even when the instances list is empty (outside QueryBoundary)', () => {
    mockInstances([])
    mockCurrentUser(['admin'])
    renderPage()

    // The button must be visible alongside the empty state — not nested inside it.
    expect(screen.getByRole('button', { name: 'Install plugin' })).toBeInTheDocument()
    expect(screen.getByText('No plugin instances')).toBeInTheDocument()
  })

  it('empty state copy changes for admin vs non-admin', () => {
    mockInstances([])
    mockCurrentUser(['admin'])
    renderPage()
    expect(screen.getByText(/Use the Install plugin button above/i)).toBeInTheDocument()
  })

  it('empty state keeps original copy for auditor', () => {
    mockInstances([])
    mockCurrentUser(['auditor'])
    renderPage()
    expect(screen.getByText(/Install a plugin to see instances here/i)).toBeInTheDocument()
  })

  // --- Per-plugin group and Add instance button ---

  it('renders "Add instance" button per plugin group for admin', () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    renderPage()

    expect(screen.getByRole('button', { name: /add instance/i })).toBeInTheDocument()
  })

  it('does NOT render "Add instance" buttons for non-admin', () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['auditor'])
    renderPage()

    expect(screen.queryByRole('button', { name: /add instance/i })).not.toBeInTheDocument()
  })

  it('clicking "Add instance" opens the modal for that plugin', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json({
          data: {
            id: 'inst-new',
            plugin_id: PLUGIN_ID,
            instance_name: 'test',
            health_state: 'healthy',
            version: 1,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          },
        }),
      ),
    )

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /add instance/i }))

    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText(/Add instance to Slack/i)).toBeInTheDocument()
  })

  // --- Install happy path ---

  it('shows success card after a successful install', async () => {
    mockInstances([])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({
          data: { id: 'plugin-new', name: 'MyPlugin', version: '0.1.0', status: 'active' },
        }),
      ),
    )

    renderPage()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File([new Uint8Array(1024)], 'plugin.tar.gz', { type: 'application/gzip' })
    await userEvent.upload(input, file)

    await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument())
    expect(screen.getByRole('status')).toHaveTextContent('MyPlugin')
  })

  // --- Install error cases ---

  it('shows 400 install error on the page', async () => {
    mockInstances([])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ error: 'bad tarball' }, { status: 400 }),
      ),
    )

    renderPage()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File([new Uint8Array(1024)], 'plugin.tar.gz', { type: 'application/gzip' })
    await userEvent.upload(input, file)

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Install failed: bad tarball')
  })

  it('shows 409 install error with verbatim server message', async () => {
    mockInstances([])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ error: 'concurrent plugin update; retry' }, { status: 409 }),
      ),
    )

    renderPage()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File([new Uint8Array(1024)], 'plugin.tar.gz', { type: 'application/gzip' })
    await userEvent.upload(input, file)

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('concurrent plugin update; retry')
  })

  it('shows 413 fixed message', async () => {
    mockInstances([])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins', () => new HttpResponse(null, { status: 413 })),
    )

    renderPage()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File([new Uint8Array(1024)], 'plugin.tar.gz', { type: 'application/gzip' })
    await userEvent.upload(input, file)

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Tarball too large')
  })

  it('shows 422 signature-invalid error', async () => {
    mockInstances([])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ error: 'Signature verification failed' }, { status: 422 }),
      ),
    )

    renderPage()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File([new Uint8Array(1024)], 'plugin.tar.gz', { type: 'application/gzip' })
    await userEvent.upload(input, file)

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Install failed: Signature verification failed')
  })

  it('shows 503 disabled notice', async () => {
    mockInstances([])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ error: 'plugin system disabled' }, { status: 503 }),
      ),
    )

    renderPage()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File([new Uint8Array(1024)], 'plugin.tar.gz', { type: 'application/gzip' })
    await userEvent.upload(input, file)

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('GLEIPNIR_PLUGINS_ENABLED=true')
  })

  // --- Create-instance happy path ---

  it('closes modal on successful create-instance', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json({
          data: {
            id: 'inst-new',
            plugin_id: PLUGIN_ID,
            instance_name: 'production',
            health_state: 'healthy',
            version: 1,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          },
        }),
      ),
    )

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /add instance/i }))

    const input = screen.getByRole('textbox')
    await userEvent.type(input, 'production')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  // --- Create-instance error cases ---

  it('shows 400 create-instance error in modal', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json({ error: 'name too long' }, { status: 400 }),
      ),
    )

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /add instance/i }))

    await userEvent.type(screen.getByRole('textbox'), 'toolongnamethatfailsvalidation')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => {
      const alerts = screen.getAllByRole('alert')
      expect(alerts.some((a) => a.textContent?.includes('Failed to create instance:'))).toBe(true)
    })
  })

  it('shows 409 create-instance error with server message', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json(
          { error: "instance named 'staging' already exists for this plugin" },
          { status: 409 },
        ),
      ),
    )

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /add instance/i }))

    await userEvent.type(screen.getByRole('textbox'), 'staging')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => {
      const alerts = screen.getAllByRole('alert')
      expect(alerts.some((a) => a.textContent?.includes("instance named 'staging' already exists"))).toBe(true)
    })
  })

  it('shows 404 create-instance error and closes modal', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json({ error: 'plugin not found' }, { status: 404 }),
      ),
    )

    vi.useFakeTimers({ shouldAdvanceTime: true })
    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /add instance/i }))

    await userEvent.type(screen.getByRole('textbox'), 'production')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => {
      const alerts = screen.getAllByRole('alert')
      expect(alerts.some((a) => a.textContent?.includes('plugin no longer exists'))).toBe(true)
    })

    await act(async () => {
      vi.advanceTimersByTime(1500)
    })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('shows 500 create-instance error in modal', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json({ error: 'internal server error' }, { status: 500 }),
      ),
    )

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /add instance/i }))

    await userEvent.type(screen.getByRole('textbox'), 'production')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => {
      const alerts = screen.getAllByRole('alert')
      expect(alerts.some((a) => a.textContent?.includes('Failed to create instance:'))).toBe(true)
    })
  })

  // --- Uninstall plugin (kebab menu) ---

  it('opens UninstallPluginModal when Uninstall plugin is clicked in kebab menu', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: /uninstall plugin/i }))

    expect(screen.getByRole('dialog')).toBeInTheDocument()
    // Instance name from the group should appear in the modal's instance list.
    expect(screen.getAllByText('slack-prod').length).toBeGreaterThanOrEqual(1)
  })

  it('fires DELETE and closes modal on successful uninstall', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    server.use(
      http.delete(`/api/v1/admin/plugins/${PLUGIN_ID}`, () => new HttpResponse(null, { status: 204 })),
    )

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /uninstall plugin/i }))

    // Click the confirm button inside the dialog (not the kebab trigger outside it).
    const dialog = screen.getByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: /uninstall plugin/i }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('shows 409 detail in UninstallPluginModal when references exist', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    server.use(
      http.delete(`/api/v1/admin/plugins/${PLUGIN_ID}`, () =>
        HttpResponse.json(
          { error: 'plugin has references', detail: 'Policy "Nightly Sync" references this plugin.' },
          { status: 409 },
        ),
      ),
    )

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /uninstall plugin/i }))

    const dialog = screen.getByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: /uninstall plugin/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Policy "Nightly Sync"')
  })

  it('does NOT render Uninstall plugin button for auditor role', () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['auditor'])
    renderPage()

    expect(screen.queryByRole('button', { name: /uninstall plugin/i })).not.toBeInTheDocument()
  })

  it('closes the kebab disclosure when Uninstall plugin is clicked', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    renderPage()

    // Simulate the disclosure being open (jsdom does not toggle open on summary click).
    const disclosure = document.querySelector('details')!
    disclosure.setAttribute('open', '')
    expect(disclosure).toHaveAttribute('open')

    await userEvent.click(screen.getByRole('button', { name: /uninstall plugin/i }))

    // The disclosure must be closed once the menu item is activated.
    expect(disclosure).not.toHaveAttribute('open')
    // And the modal should now be visible.
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('closes the kebab disclosure when the modal is cancelled', async () => {
    mockInstances([INSTANCE_HEALTHY])
    mockCurrentUser(['admin'])
    renderPage()

    const disclosure = document.querySelector('details')!
    disclosure.setAttribute('open', '')

    // Open the modal by clicking the menu item (which also closes the disclosure).
    await userEvent.click(screen.getByRole('button', { name: /uninstall plugin/i }))

    expect(screen.getByRole('dialog')).toBeInTheDocument()
    // Disclosure is already closed at this point (closed when menu item is clicked).
    expect(disclosure).not.toHaveAttribute('open')

    // Cancel the modal — disclosure must remain closed.
    await userEvent.click(screen.getByRole('button', { name: /cancel/i }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(disclosure).not.toHaveAttribute('open')
  })
})

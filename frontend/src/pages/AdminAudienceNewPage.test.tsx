import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router'

import AdminAudienceNewPage from './AdminAudienceNewPage'
import type { ApiPluginInstanceForAudience } from '@/api/types'

// --- Mocks ---

vi.mock('@/hooks/queries/admin')
vi.mock('@/hooks/mutations/admin')
vi.mock('@/hooks/queries/users')

import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { useCreateAudience } from '@/hooks/mutations/admin'
import { useCurrentUser } from '@/hooks/queries/users'

const mockNavigate = vi.fn()
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router')
  return { ...actual, useNavigate: () => mockNavigate }
})

// --- Fixtures ---

const ADMIN_USER = { id: 'u1', username: 'alice', roles: ['admin'] }
const AUDITOR_USER = { id: 'u2', username: 'bob', roles: ['auditor'] }

const PLUGINS: ApiPluginInstanceForAudience[] = [
  {
    id: 'slack-1',
    plugin_id: 'com.example.slack',
    instance_name: 'slack-1',
    state: 'healthy',
    implements_notify: true,
    implements_request: false,
    config_schema: null,
    version: 0,
  },
]

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderPage(queryClient = makeQueryClient()) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <AdminAudienceNewPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function mockPluginsLoaded(plugins: ApiPluginInstanceForAudience[]) {
  vi.mocked(usePluginInstancesForAudience).mockReturnValue({
    data: plugins,
    status: 'success',
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof usePluginInstancesForAudience>)
}

function mockPluginsPending() {
  vi.mocked(usePluginInstancesForAudience).mockReturnValue({
    data: undefined,
    status: 'pending',
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof usePluginInstancesForAudience>)
}

function mockCurrentUser(user: typeof ADMIN_USER | null) {
  vi.mocked(useCurrentUser).mockReturnValue({
    data: user,
    status: user ? 'success' : 'pending',
  } as unknown as ReturnType<typeof useCurrentUser>)
}

function mockCreateNoop() {
  vi.mocked(useCreateAudience).mockReturnValue({
    mutateAsync: vi.fn(),
    isPending: false,
    error: null,
    reset: vi.fn(),
  } as unknown as ReturnType<typeof useCreateAudience>)
}

// --- Tests ---

describe('AdminAudienceNewPage — loading state', () => {
  beforeEach(() => {
    mockPluginsPending()
    mockCurrentUser(ADMIN_USER)
    mockCreateNoop()
  })

  it('shows skeleton while plugin instances load', () => {
    renderPage()
    const skeletons = document.querySelectorAll('[aria-hidden="true"]')
    expect(skeletons.length).toBeGreaterThan(0)
  })
})

describe('AdminAudienceNewPage — create mode', () => {
  beforeEach(() => {
    mockPluginsLoaded(PLUGINS)
    mockCurrentUser(ADMIN_USER)
    mockCreateNoop()
  })

  it('renders page title New Audience', () => {
    renderPage()
    expect(screen.getByRole('heading', { name: 'New Audience' })).toBeInTheDocument()
  })

  it('renders editor in create mode (no Delete button)', () => {
    renderPage()
    expect(screen.queryByRole('button', { name: /delete audience/i })).not.toBeInTheDocument()
  })

  it('renders Save button', () => {
    renderPage()
    expect(screen.getByRole('button', { name: /^save$/i })).toBeInTheDocument()
  })

  it('renders back link to /admin/audiences', () => {
    renderPage()
    const link = screen.getByRole('link', { name: /all audiences/i })
    expect(link).toHaveAttribute('href', '/admin/audiences')
  })
})

describe('AdminAudienceNewPage — auditor redirect', () => {
  it('redirects auditors to /admin/audiences', async () => {
    mockPluginsLoaded(PLUGINS)
    mockCurrentUser(AUDITOR_USER)
    mockCreateNoop()

    renderPage()

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/admin/audiences', { replace: true })
    })
  })
})

describe('AdminAudienceNewPage — successful save navigates', () => {
  it('navigates to /admin/audiences/:id after successful create', async () => {
    mockPluginsLoaded(PLUGINS)
    mockCurrentUser(ADMIN_USER)

    const createdAudience = {
      id: 'new-aud-id',
      name: 'test',
      disable_in_app_fallback: false,
      version: 1,
      created_at: '',
      updated_at: '',
      entries: [],
    }

    vi.mocked(useCreateAudience).mockReturnValue({
      mutateAsync: vi.fn().mockResolvedValue(createdAudience),
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useCreateAudience>)

    renderPage()

    fireEvent.change(screen.getByLabelText(/audience name/i), { target: { value: 'test' } })
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/admin/audiences/new-aud-id')
    })
  })
})

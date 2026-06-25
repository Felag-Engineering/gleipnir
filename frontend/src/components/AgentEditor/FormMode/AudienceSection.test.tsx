import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'

import { server } from '@/test/server'
import { AudienceSection } from './AudienceSection'
import type { AudienceFormState } from './types'
import type { ApiAudienceListItem, ApiAudience } from '@/api/types'

// --- Fixtures ---

const AUDIENCE_OPS: ApiAudienceListItem = {
  id: 'aud-1',
  name: 'ops-team',
  entry_count: 2,
  referenced_by_policy_count: 1,
  has_in_flight_runs: false,
  disable_in_app_fallback: false,
  version: 1,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-04-01T00:00:00Z',
}

const AUDIENCE_SECURITY: ApiAudienceListItem = {
  id: 'aud-2',
  name: 'security-team',
  entry_count: 1,
  referenced_by_policy_count: 0,
  has_in_flight_runs: false,
  disable_in_app_fallback: true,
  version: 1,
  created_at: '2026-02-01T00:00:00Z',
  updated_at: '2026-04-15T00:00:00Z',
}

const AUDIENCE_LIST = [AUDIENCE_OPS, AUDIENCE_SECURITY]

const AUDIENCE_OPS_DETAIL: ApiAudience = {
  id: 'aud-1',
  name: 'ops-team',
  disable_in_app_fallback: false,
  version: 1,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-04-01T00:00:00Z',
  entries: [
    {
      id: 'e1',
      plugin_instance_id: 'slack-ops',
      position: 0,
      notify: true,
      request: false,
      config: { channel: '#ops' },
    },
    {
      id: 'e2',
      plugin_instance_id: 'pagerduty-main',
      position: 1,
      notify: false,
      request: true,
      config: {},
    },
  ],
}

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderSection(
  value: AudienceFormState,
  onChange?: (next: AudienceFormState) => void,
  onNewAudienceClick?: () => void,
) {
  const handleChange = onChange ?? vi.fn()
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter>
        <AudienceSection
          value={value}
          onChange={handleChange}
          onNewAudienceClick={onNewAudienceClick}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// Controlled wrapper to observe onChange calls and reflect them in the DOM.
function ControlledAudienceSection({
  initial,
  onChange,
  onNewAudienceClick,
}: {
  initial: AudienceFormState
  onChange?: (next: AudienceFormState) => void
  onNewAudienceClick?: () => void
}) {
  const [value, setValue] = useState(initial)
  function handleChange(next: AudienceFormState) {
    setValue(next)
    onChange?.(next)
  }
  return (
    <AudienceSection
      value={value}
      onChange={handleChange}
      onNewAudienceClick={onNewAudienceClick}
    />
  )
}

function renderControlled(
  initial: AudienceFormState,
  onChange?: (next: AudienceFormState) => void,
  onNewAudienceClick?: () => void,
) {
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter>
        <ControlledAudienceSection
          initial={initial}
          onChange={onChange}
          onNewAudienceClick={onNewAudienceClick}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// --- Tests ---

describe('AudienceSection — rendering', () => {
  it('renders the Audience heading', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: [] }),
      ),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    renderSection({ name: '' })
    expect(screen.getByText('Audience')).toBeInTheDocument()
  })

  it('renders a <select> with — None — as the first option', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: [] }),
      ),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    renderSection({ name: '' })
    const select = screen.getByRole('combobox', { name: /audience/i })
    expect(select).toBeInTheDocument()
    expect(screen.getByRole('option', { name: '— None —' })).toBeInTheDocument()
  })

  it('renders audience options from the API response', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: AUDIENCE_LIST }),
      ),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    renderSection({ name: '' })
    await waitFor(() => {
      expect(screen.getByRole('option', { name: 'ops-team' })).toBeInTheDocument()
      expect(screen.getByRole('option', { name: 'security-team' })).toBeInTheDocument()
    })
  })

  it('shows "No audience selected." hint when value.name is empty', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: [] }),
      ),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    renderSection({ name: '' })
    await waitFor(() => {
      expect(screen.getByText('No audience selected.')).toBeInTheDocument()
    })
  })
})

describe('AudienceSection — selection callbacks', () => {
  it('calls onChange with the selected name when an option is chosen', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: AUDIENCE_LIST }),
      ),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    const onChange = vi.fn()
    renderSection({ name: '' }, onChange)

    await waitFor(() => {
      expect(screen.getByRole('option', { name: 'ops-team' })).toBeInTheDocument()
    })

    const select = screen.getByRole('combobox', { name: /audience/i })
    fireEvent.change(select, { target: { value: 'ops-team' } })

    expect(onChange).toHaveBeenCalledWith({ name: 'ops-team' })
  })

  it('calls onChange with { name: "" } when — None — is selected', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: AUDIENCE_LIST }),
      ),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    const onChange = vi.fn()
    renderSection({ name: 'ops-team' }, onChange)

    const select = screen.getByRole('combobox', { name: /audience/i })
    fireEvent.change(select, { target: { value: '' } })

    expect(onChange).toHaveBeenCalledWith({ name: '' })
  })
})

describe('AudienceSection — + New audience link', () => {
  it('calls onNewAudienceClick when the link is clicked', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: [] }),
      ),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    const onNewAudienceClick = vi.fn()
    renderSection({ name: '' }, undefined, onNewAudienceClick)

    fireEvent.click(screen.getByRole('button', { name: '+ New audience' }))
    expect(onNewAudienceClick).toHaveBeenCalledTimes(1)
  })
})

describe('AudienceSection — preview area state machine', () => {
  it('shows "Resolving routing…" placeholder while list is loading and name is set — state (a)', async () => {
    server.use(
      // Never resolves — simulates loading state.
      http.get('/api/v1/admin/audiences', () => new Promise(() => {})),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    renderSection({ name: 'ops-team' })
    // The text appears immediately because the query starts loading.
    expect(screen.getByText('Resolving routing…')).toBeInTheDocument()
    // Must NOT show the "not available" warning yet.
    expect(screen.queryByText(/is no longer available/)).not.toBeInTheDocument()
  })

  it('shows "is no longer available" warning when list is loaded but name has no match — state (b)', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: AUDIENCE_LIST }),
      ),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    renderSection({ name: 'ghost-audience' })
    await waitFor(() => {
      expect(screen.getByText(/is no longer available/)).toBeInTheDocument()
    })
    // Must NOT show placeholder.
    expect(screen.queryByText('Resolving routing…')).not.toBeInTheDocument()
  })

  it('shows RoutingPreview content when audience is selected and detail loads — state (c)', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: AUDIENCE_LIST }),
      ),
      http.get('/api/v1/admin/audiences/:id', ({ params }) => {
        if (params.id === 'aud-1') {
          return HttpResponse.json({ data: AUDIENCE_OPS_DETAIL })
        }
        return HttpResponse.json({ error: 'not found' }, { status: 404 })
      }),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    renderSection({ name: 'ops-team' })
    // RoutingPreview renders these labels
    await waitFor(() => {
      expect(screen.getByText(/Notifications fan out to:/)).toBeInTheDocument()
      expect(screen.getByText(/Requests routed to:/)).toBeInTheDocument()
    })
  })

  it('shows "Resolving routing…" while detail is pending after list has loaded', async () => {
    server.use(
      http.get('/api/v1/admin/audiences', () =>
        HttpResponse.json({ data: AUDIENCE_LIST }),
      ),
      // Detail never resolves — simulates in-flight detail fetch.
      http.get('/api/v1/admin/audiences/:id', () => new Promise(() => {})),
      http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
    )
    renderSection({ name: 'ops-team' })
    // After list loads, detail is still in flight — show placeholder, not warning.
    await waitFor(() => {
      expect(screen.queryByText(/is no longer available/)).not.toBeInTheDocument()
    })
    expect(screen.getByText('Resolving routing…')).toBeInTheDocument()
  })
})

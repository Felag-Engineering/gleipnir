import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { AudienceSection } from './AudienceSection'
import type { AudienceFormState } from './types'
import type { ApiAudienceListItem, ApiAudience } from '@/api/types'
import decoratorStyles from './AudienceSection.stories.module.css'

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

const AUDIENCE_ONCALL: ApiAudienceListItem = {
  id: 'aud-3',
  name: 'on-call',
  entry_count: 3,
  referenced_by_policy_count: 2,
  has_in_flight_runs: false,
  disable_in_app_fallback: false,
  version: 2,
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-05-01T00:00:00Z',
}

const ALL_AUDIENCES = [AUDIENCE_OPS, AUDIENCE_SECURITY, AUDIENCE_ONCALL]

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

function Decorator({ children }: { children: React.ReactNode }) {
  return (
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter>
        <div className={decoratorStyles.decorator}>{children}</div>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

// --- Meta ---

const meta: Meta<typeof AudienceSection> = {
  title: 'PolicyEditor/FormMode/AudienceSection',
  component: AudienceSection,
  decorators: [
    (Story) => (
      <Decorator>
        <Story />
      </Decorator>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof AudienceSection>

// --- Stories ---

// NoneSelected — empty audience list, no selection.
export const NoneSelected: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/audiences', () =>
          HttpResponse.json({ data: [] }),
        ),
        http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
      ],
    },
  },
  args: {
    value: { name: '' },
    onChange: fn(),
  },
}

// WithAudiences — list of audiences, nothing selected yet.
export const WithAudiences: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/audiences', () =>
          HttpResponse.json({ data: ALL_AUDIENCES }),
        ),
        http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
      ],
    },
  },
  args: {
    value: { name: '' },
    onChange: fn(),
  },
}

// Selected — ops-team selected with a populated RoutingPreview.
export const Selected: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/audiences', () =>
          HttpResponse.json({ data: ALL_AUDIENCES }),
        ),
        http.get('/api/v1/admin/audiences/:id', ({ params }) => {
          if (params.id === 'aud-1') {
            return HttpResponse.json({ data: AUDIENCE_OPS_DETAIL })
          }
          return HttpResponse.json({ error: 'not found' }, { status: 404 })
        }),
        http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
      ],
    },
  },
  args: {
    value: { name: 'ops-team' },
    onChange: fn(),
  },
}

// SelectedMissing — audience name is set but no matching item in the list (warning state).
export const SelectedMissing: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/audiences', () =>
          HttpResponse.json({ data: ALL_AUDIENCES }),
        ),
        http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
      ],
    },
  },
  args: {
    value: { name: 'deleted-audience' },
    onChange: fn(),
  },
}

// Loading — list is still pending while a name is set (placeholder state).
export const Loading: Story = {
  parameters: {
    msw: {
      handlers: [
        // Never resolves — keeps the query in loading state.
        http.get('/api/v1/admin/audiences', () => new Promise(() => {})),
        http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
      ],
    },
  },
  args: {
    value: { name: 'ops-team' },
    onChange: fn(),
  },
}

// Interactive — local state so the dropdown is fully exercisable.
function InteractiveAudienceSection() {
  const [value, setValue] = useState<AudienceFormState>({ name: '' })
  return (
    <AudienceSection
      value={value}
      onChange={setValue}
    />
  )
}

export const Interactive: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/audiences', () =>
          HttpResponse.json({ data: ALL_AUDIENCES }),
        ),
        http.get('/api/v1/admin/audiences/:id', ({ params }) => {
          if (params.id === 'aud-1') {
            return HttpResponse.json({ data: AUDIENCE_OPS_DETAIL })
          }
          return HttpResponse.json({ error: 'not found' }, { status: 404 })
        }),
        http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
      ],
    },
  },
  render: () => <InteractiveAudienceSection />,
}

import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { AudienceEditor } from './AudienceEditor'
import type {
  ApiAudience,
  ApiAudienceReferences,
  ApiPluginInstanceForAudience,
} from '@/api/types'
import { ApiError } from '@/api/fetch'

// Fixtures

const PLUGIN_SLACK: ApiPluginInstanceForAudience = {
  id: 'slack-primary',
  plugin_id: 'com.example.slack',
  instance_name: 'slack-primary',
  state: 'healthy',
  implements_notify: true,
  implements_request: true,
  version: 0,
  config_schema: {
    type: 'object',
    properties: {
      channel: {
        type: 'string',
        title: 'Channel',
        description: 'Slack channel name (e.g. #ops)',
        'x-gleipnir-options': { source: 'channels' },
      },
      mention: { type: 'string', title: 'Mention', description: 'User or group to @-mention' },
      response_buttons: {
        type: 'array',
        items: { type: 'object' },
        description: 'Action buttons shown to the recipient. Defaults to Approve/Reject when omitted.',
      },
    },
    required: ['channel'],
  },
}

const PLUGIN_NTFY: ApiPluginInstanceForAudience = {
  id: 'ntfy-backup',
  plugin_id: 'com.example.ntfy',
  instance_name: 'ntfy-backup',
  state: 'unsigned_permissive',
  implements_notify: true,
  implements_request: false,
  version: 0,
  config_schema: {
    type: 'object',
    properties: {
      topic: { type: 'string', title: 'Topic', description: 'ntfy topic name' },
    },
    required: ['topic'],
  },
}

const PLUGIN_PAGERDUTY: ApiPluginInstanceForAudience = {
  id: 'pagerduty-main',
  plugin_id: 'com.example.pagerduty',
  instance_name: 'pagerduty-main',
  state: 'healthy',
  implements_notify: false,
  implements_request: true,
  version: 0,
  config_schema: null,
}

const ALL_PLUGINS = [PLUGIN_SLACK, PLUGIN_NTFY, PLUGIN_PAGERDUTY]

const AUDIENCE_SINGLE: ApiAudience = {
  id: 'aud-1',
  name: 'ops-team',
  disable_in_app_fallback: false,
  version: 3,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-04-01T12:00:00Z',
  entries: [
    {
      id: 'e1',
      plugin_instance_id: 'slack-primary',
      position: 0,
      notify: true,
      request: true,
      config: { channel: '#ops' },
    },
  ],
}

const AUDIENCE_WITH_RESPONSE_BUTTONS: ApiAudience = {
  id: 'aud-rb',
  name: 'approval-team',
  disable_in_app_fallback: false,
  version: 2,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-04-01T12:00:00Z',
  entries: [
    {
      id: 'e1',
      plugin_instance_id: 'slack-primary',
      position: 0,
      notify: true,
      request: true,
      config: {
        channel: '#approvals',
        response_buttons: [
          { option_id: 'approve', label: 'Approve', value: 'approved', style: 'primary' },
          { option_id: 'reject', label: 'Reject', value: 'rejected', style: 'danger' },
        ],
      },
    },
  ],
}

// MSW handler for the Slack channels options provider.
const SLACK_CHANNELS_HANDLER = http.get(
  '/api/v1/admin/plugins/:pluginId/instances/:instanceId/options/channels',
  () =>
    HttpResponse.json({
      data: {
        options: [
          { value: '#ops', label: '#ops' },
          { value: '#incidents', label: '#incidents' },
          { value: '#approvals', label: '#approvals' },
          { value: '#general', label: '#general' },
        ],
        next_cursor: '',
      },
    }),
)

const AUDIENCE_MULTI: ApiAudience = {
  id: 'aud-2',
  name: 'all-hands',
  disable_in_app_fallback: false,
  version: 7,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-04-15T09:00:00Z',
  entries: [
    {
      id: 'e1',
      plugin_instance_id: 'slack-primary',
      position: 0,
      notify: true,
      request: false,
      config: { channel: '#incidents' },
    },
    {
      id: 'e2',
      plugin_instance_id: 'ntfy-backup',
      position: 1,
      notify: true,
      request: false,
      config: {},
    },
    {
      id: 'e3',
      plugin_instance_id: 'pagerduty-main',
      position: 2,
      notify: false,
      request: true,
      config: {},
    },
  ],
}

const REFS_EMPTY: ApiAudienceReferences = {
  policies: [],
  in_flight_runs: [],
}

const REFS_WITH_POLICIES: ApiAudienceReferences = {
  policies: [
    { id: 'p1', name: 'deploy-bot' },
    { id: 'p2', name: 'smoke-tests' },
  ],
  in_flight_runs: [{ id: 'run-abc123', policy_id: 'p1', status: 'running' }],
}

function wrap(children: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <div style={{ maxWidth: 900, padding: '24px' }}>{children}</div>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

const meta: Meta<typeof AudienceEditor> = {
  title: 'Admin/AudienceEditor/AudienceEditor',
  component: AudienceEditor,
}

export default meta
type Story = StoryObj<typeof AudienceEditor>

export const EmptyAudience: Story = {
  render: () =>
    wrap(
      <AudienceEditor
        initial={null}
        pluginInstances={ALL_PLUGINS}
        references={null}
        canManage={true}
        onSave={async (req) => {
          console.log('save', req)
          return { id: 'new', name: 'new', disable_in_app_fallback: false, version: 1, created_at: '', updated_at: '', entries: [] }
        }}
        saveError={null}
        deleteError={null}
      />,
    ),
}

export const SingleEntry: Story = {
  render: () =>
    wrap(
      <AudienceEditor
        initial={AUDIENCE_SINGLE}
        pluginInstances={ALL_PLUGINS}
        references={REFS_EMPTY}
        canManage={true}
        onSave={async (req) => { console.log('save', req); return AUDIENCE_SINGLE }}
        onDelete={async () => { console.log('delete') }}
        saveError={null}
        deleteError={null}
      />,
    ),
}

export const MultiEntryMixedCapabilities: Story = {
  render: () =>
    wrap(
      <AudienceEditor
        initial={AUDIENCE_MULTI}
        pluginInstances={ALL_PLUGINS}
        references={REFS_EMPTY}
        canManage={true}
        onSave={async (req) => { console.log('save', req); return AUDIENCE_MULTI }}
        onDelete={async () => { console.log('delete') }}
        saveError={null}
        deleteError={null}
      />,
    ),
}

export const WithReferences: Story = {
  render: () =>
    wrap(
      <AudienceEditor
        initial={AUDIENCE_MULTI}
        pluginInstances={ALL_PLUGINS}
        references={REFS_WITH_POLICIES}
        canManage={true}
        onSave={async (req) => { console.log('save', req); return AUDIENCE_MULTI }}
        onDelete={async () => { console.log('delete') }}
        saveError={null}
        deleteError={null}
      />,
    ),
}

export const ReadOnlyAuditor: Story = {
  render: () =>
    wrap(
      <AudienceEditor
        initial={AUDIENCE_MULTI}
        pluginInstances={ALL_PLUGINS}
        references={REFS_WITH_POLICIES}
        canManage={false}
        onSave={async () => AUDIENCE_MULTI}
        saveError={null}
        deleteError={null}
      />,
    ),
}

export const VersionConflict: Story = {
  render: () =>
    wrap(
      <AudienceEditor
        initial={AUDIENCE_SINGLE}
        pluginInstances={ALL_PLUGINS}
        references={REFS_EMPTY}
        canManage={true}
        onSave={async () => { throw new ApiError(409, 'run status transition lost to concurrent writer') }}
        onDelete={async () => {}}
        saveError={new ApiError(409, 'run status transition lost to concurrent writer')}
        deleteError={null}
      />,
    ),
}

// SlackWithAsyncCombobox: channel renders as AsyncCombobox (searchable select).
// MSW intercepts the options endpoint and returns sample channels.
export const SlackWithAsyncCombobox: Story = {
  parameters: {
    msw: { handlers: [SLACK_CHANNELS_HANDLER] },
  },
  render: () =>
    wrap(
      <AudienceEditor
        initial={AUDIENCE_SINGLE}
        pluginInstances={[PLUGIN_SLACK, PLUGIN_PAGERDUTY]}
        references={REFS_EMPTY}
        canManage={true}
        onSave={async (req) => { console.log('save', req); return AUDIENCE_SINGLE }}
        onDelete={async () => { console.log('delete') }}
        saveError={null}
        deleteError={null}
      />,
    ),
}

// SlackWithResponseButtons: shows the response_buttons escape-hatch editor.
export const SlackWithResponseButtons: Story = {
  parameters: {
    msw: { handlers: [SLACK_CHANNELS_HANDLER] },
  },
  render: () =>
    wrap(
      <AudienceEditor
        initial={AUDIENCE_WITH_RESPONSE_BUTTONS}
        pluginInstances={[PLUGIN_SLACK, PLUGIN_PAGERDUTY]}
        references={REFS_EMPTY}
        canManage={true}
        onSave={async (req) => { console.log('save', req); return AUDIENCE_WITH_RESPONSE_BUTTONS }}
        onDelete={async () => { console.log('delete') }}
        saveError={null}
        deleteError={null}
      />,
    ),
}

// SlackDegradedFreeText: options provider is unavailable — channel falls back to plain text.
export const SlackDegradedFreeText: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get(
          '/api/v1/admin/plugins/:pluginId/instances/:instanceId/options/channels',
          () => HttpResponse.json({ data: { options: [], next_cursor: '', degraded: true } }),
        ),
      ],
    },
  },
  render: () =>
    wrap(
      <AudienceEditor
        initial={AUDIENCE_SINGLE}
        pluginInstances={[PLUGIN_SLACK]}
        references={REFS_EMPTY}
        canManage={true}
        onSave={async (req) => { console.log('save', req); return AUDIENCE_SINGLE }}
        onDelete={async () => { console.log('delete') }}
        saveError={null}
        deleteError={null}
      />,
    ),
}

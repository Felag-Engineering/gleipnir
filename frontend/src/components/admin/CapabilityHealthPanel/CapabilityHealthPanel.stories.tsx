import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { CapabilityHealthPanel } from './CapabilityHealthPanel'
import type { ApiPluginCapabilityHealth } from '@/api/types'

const PLUGIN_ID = 'plugin-slack-01'
const INSTANCE_ID = 'inst-slack-prod'
const URL = `/api/v1/admin/plugins/${PLUGIN_ID}/instances/${INSTANCE_ID}/capabilities`

const MIXED: ApiPluginCapabilityHealth[] = [
  { profile: 'event_source', name: 'channel_message', state: 'unhealthy', detail: 'missing scope: channels:history', source: 'self_report' },
  { profile: 'event_source', name: 'direct_message', state: 'healthy', detail: '', source: 'probe' },
  { profile: 'human_channel', name: '', state: 'pending_reauthorize', detail: 'token refresh failed', source: 'probe' },
  { profile: 'tool_provider', name: '', state: 'healthy', detail: '', source: 'probe' },
]

function Wrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return (
    <QueryClientProvider client={qc}>
      <CapabilityHealthPanel pluginId={PLUGIN_ID} instanceId={INSTANCE_ID} />
    </QueryClientProvider>
  )
}

const meta: Meta<typeof CapabilityHealthPanel> = {
  title: 'Admin/CapabilityHealthPanel',
  component: CapabilityHealthPanel,
  render: () => <Wrapper />,
}

export default meta
type Story = StoryObj<typeof CapabilityHealthPanel>

export const MixedHealth: Story = {
  parameters: {
    msw: { handlers: [http.get(URL, () => HttpResponse.json({ data: MIXED }))] },
  },
}

export const Empty: Story = {
  parameters: {
    msw: { handlers: [http.get(URL, () => HttpResponse.json({ data: [] }))] },
  },
}

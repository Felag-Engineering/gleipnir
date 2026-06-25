import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { RoutingPreview } from './RoutingPreview'
import type { ApiAudienceEntry, ApiPluginInstanceForAudience } from '@/api/types'

const meta: Meta<typeof RoutingPreview> = {
  title: 'Admin/AudienceEditor/RoutingPreview',
  component: RoutingPreview,
}

export default meta
type Story = StoryObj<typeof RoutingPreview>

const notifyEntry = (id: string, pluginId: string): ApiAudienceEntry => ({
  id,
  plugin_instance_id: pluginId,
  position: 0,
  notify: true,
  request: false,
  config: {},
})

const requestEntry = (id: string, pluginId: string): ApiAudienceEntry => ({
  id,
  plugin_instance_id: pluginId,
  position: 1,
  notify: false,
  request: true,
  config: {},
})

const bothEntry = (id: string, pluginId: string): ApiAudienceEntry => ({
  id,
  plugin_instance_id: pluginId,
  position: 0,
  notify: true,
  request: true,
  config: {},
})

const autoEntry: ApiAudienceEntry = {
  id: '__auto__',
  plugin_instance_id: '',
  position: 99,
  notify: true,
  request: true,
  config: {},
  auto: true,
}

const pluginInstance = (id: string, instanceName: string): ApiPluginInstanceForAudience => ({
  id,
  plugin_id: 'com.example.plugin',
  instance_name: instanceName,
  state: 'healthy',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  version: 1,
})

export const Empty: Story = {
  args: {
    entries: [],
    disableInAppFallback: true,
    pluginInstances: [],
  },
}

export const NotifyOnly: Story = {
  args: {
    entries: [notifyEntry('e1', 'slack-primary')],
    disableInAppFallback: false,
    pluginInstances: [pluginInstance('slack-primary', 'slack-e2e')],
  },
}

export const RequestOnly: Story = {
  args: {
    entries: [requestEntry('e1', 'pagerduty-main')],
    disableInAppFallback: false,
    pluginInstances: [pluginInstance('pagerduty-main', 'pagerduty-prod')],
  },
}

export const Mixed: Story = {
  args: {
    entries: [
      notifyEntry('e1', 'slack-primary'),
      notifyEntry('e2', 'ntfy-backup'),
      requestEntry('e3', 'pagerduty-main'),
    ],
    disableInAppFallback: false,
    pluginInstances: [
      pluginInstance('slack-primary', 'slack-e2e'),
      pluginInstance('ntfy-backup', 'ntfy-ops'),
      pluginInstance('pagerduty-main', 'pagerduty-prod'),
    ],
  },
}

export const WithAutoEntry: Story = {
  args: {
    entries: [notifyEntry('e1', 'slack-primary'), autoEntry],
    disableInAppFallback: false,
    pluginInstances: [pluginInstance('slack-primary', 'slack-e2e')],
  },
}

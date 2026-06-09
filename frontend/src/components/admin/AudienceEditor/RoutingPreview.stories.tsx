import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { RoutingPreview } from './RoutingPreview'
import type { ApiAudienceEntry } from '@/api/types'

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

export const Empty: Story = {
  args: {
    entries: [],
    disableInAppFallback: true,
  },
}

export const NotifyOnly: Story = {
  args: {
    entries: [notifyEntry('e1', 'slack-primary')],
    disableInAppFallback: false,
  },
}

export const RequestOnly: Story = {
  args: {
    entries: [requestEntry('e1', 'pagerduty-main')],
    disableInAppFallback: false,
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
  },
}

export const WithAutoEntry: Story = {
  args: {
    entries: [notifyEntry('e1', 'slack-primary'), autoEntry],
    disableInAppFallback: false,
  },
}

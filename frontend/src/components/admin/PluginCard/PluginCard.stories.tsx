import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { PluginCard } from './PluginCard'

const meta: Meta<typeof PluginCard> = {
  title: 'Admin/PluginCard',
  component: PluginCard,
  parameters: {
    layout: 'padded',
  },
  args: {
    onClick: () => {},
  },
}

export default meta
type Story = StoryObj<typeof PluginCard>

// All three services declared, 3 instances, healthy, not selected.
export const AllServices: Story = {
  args: {
    pluginName: 'Slack',
    pluginVersion: '1.2.0',
    pluginId: 'plugin-example-01',
    services: ['tool', 'trigger', 'channel'],
    instanceCount: 3,
    aggregateHealth: 'healthy',
    hasSbom: false,
    isSelected: false,
  },
}

// Tool-only plugin, 1 instance, unhealthy, not selected.
export const ToolOnly: Story = {
  args: {
    pluginName: 'GitHub',
    pluginVersion: '0.9.1',
    pluginId: 'plugin-example-02',
    services: ['tool'],
    instanceCount: 1,
    aggregateHealth: 'unhealthy',
    hasSbom: false,
    isSelected: false,
  },
}

// Trigger + channel, 2 instances, pending_key_approval, not selected.
export const TriggerAndChannel: Story = {
  args: {
    pluginName: 'PagerDuty',
    pluginVersion: '2.0.0',
    pluginId: 'plugin-example-03',
    services: ['trigger', 'channel'],
    instanceCount: 2,
    aggregateHealth: 'pending_key_approval',
    hasSbom: false,
    isSelected: false,
  },
}

// Tool + channel, 2 instances, healthy, isSelected=true — shows the blue
// selected border and elevated background.
export const Selected: Story = {
  args: {
    pluginName: 'Jira',
    pluginVersion: '1.1.0',
    pluginId: 'plugin-example-04',
    services: ['tool', 'channel'],
    instanceCount: 2,
    aggregateHealth: 'healthy',
    hasSbom: false,
    isSelected: true,
  },
}

// Plugin with an SBOM declared — shows the green "SBOM" informational badge
// appended after the service badges. The badge is a plain <span> (not a link)
// because the card has role="button"; the clickable download link lives in the
// right-pane detail panel on AdminPluginsPage.
export const WithSBOM: Story = {
  args: {
    pluginName: 'Slack',
    pluginVersion: '1.2.0',
    pluginId: 'plugin-example-05',
    services: ['tool', 'trigger', 'channel'],
    instanceCount: 2,
    aggregateHealth: 'healthy',
    hasSbom: true,
    isSelected: false,
  },
}

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
    services: ['tool', 'trigger', 'channel'],
    instanceCount: 3,
    aggregateHealth: 'healthy',
    isSelected: false,
  },
}

// Tool-only plugin, 1 instance, unhealthy, not selected.
export const ToolOnly: Story = {
  args: {
    pluginName: 'GitHub',
    pluginVersion: '0.9.1',
    services: ['tool'],
    instanceCount: 1,
    aggregateHealth: 'unhealthy',
    isSelected: false,
  },
}

// Trigger + channel, 2 instances, pending_key_approval, not selected.
export const TriggerAndChannel: Story = {
  args: {
    pluginName: 'PagerDuty',
    pluginVersion: '2.0.0',
    services: ['trigger', 'channel'],
    instanceCount: 2,
    aggregateHealth: 'pending_key_approval',
    isSelected: false,
  },
}

// Tool + channel, 2 instances, healthy, isSelected=true — shows the blue
// selected border and elevated background.
export const Selected: Story = {
  args: {
    pluginName: 'Jira',
    pluginVersion: '1.1.0',
    services: ['tool', 'channel'],
    instanceCount: 2,
    aggregateHealth: 'healthy',
    isSelected: true,
  },
}

import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import ConnectionBanner from './ConnectionBanner'

const meta: Meta<typeof ConnectionBanner> = {
  title: 'Components/ConnectionBanner',
  component: ConnectionBanner,
}

export default meta
type Story = StoryObj<typeof ConnectionBanner>

export const Connected: Story = {
  args: {
    state: 'connected',
    compact: false,
  },
}

export const Reconnecting: Story = {
  args: {
    state: 'reconnecting',
    compact: false,
  },
}

export const Disconnected: Story = {
  args: {
    state: 'disconnected',
    compact: false,
  },
}

export const CompactConnected: Story = {
  args: {
    state: 'connected',
    compact: true,
  },
}

export const CompactReconnecting: Story = {
  args: {
    state: 'reconnecting',
    compact: true,
  },
}

export const CompactDisconnected: Story = {
  args: {
    state: 'disconnected',
    compact: true,
  },
}

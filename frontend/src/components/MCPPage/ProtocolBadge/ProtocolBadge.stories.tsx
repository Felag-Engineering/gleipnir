import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ProtocolBadge } from './ProtocolBadge'

const meta: Meta<typeof ProtocolBadge> = {
  title: 'ToolsPage/ProtocolBadge',
  component: ProtocolBadge,
}

export default meta
type Story = StoryObj<typeof ProtocolBadge>

export const Modern: Story = {
  args: { version: '2026-07-28' },
}

export const Legacy: Story = {
  args: { version: '2024-11-05' },
}

export const Unknown: Story = {
  args: { version: null },
}

import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { HealthIndicator } from './HealthIndicator'

const meta: Meta<typeof HealthIndicator> = {
  title: 'MCPPage/HealthIndicator',
  component: HealthIndicator,
}

export default meta
type Story = StoryObj<typeof HealthIndicator>

export const Connected: Story = { args: { status: 'connected' } }
export const Unreachable: Story = { args: { status: 'unreachable' } }
export const Discovering: Story = { args: { status: 'discovering' } }

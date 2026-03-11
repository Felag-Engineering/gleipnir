import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { MCPStatsBar } from './MCPStatsBar'

const meta: Meta<typeof MCPStatsBar> = {
  title: 'MCPPage/MCPStatsBar',
  component: MCPStatsBar,
}

export default meta
type Story = StoryObj<typeof MCPStatsBar>

export const Loaded: Story = {
  args: { totalTools: 15, sensors: 9, actuators: 5, feedback: 1, isLoading: false },
}

export const Loading: Story = {
  args: { totalTools: 0, sensors: 0, actuators: 0, feedback: 0, isLoading: true },
}

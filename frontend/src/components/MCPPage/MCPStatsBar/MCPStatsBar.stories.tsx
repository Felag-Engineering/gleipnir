import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { MCPStatsBar } from './MCPStatsBar'

const meta: Meta<typeof MCPStatsBar> = {
  title: 'ToolsPage/MCPStatsBar',
  component: MCPStatsBar,
}

export default meta
type Story = StoryObj<typeof MCPStatsBar>

export const Loaded: Story = {
  args: { totalTools: 15, isLoading: false },
}

export const Loading: Story = {
  args: { totalTools: 0, isLoading: true },
}

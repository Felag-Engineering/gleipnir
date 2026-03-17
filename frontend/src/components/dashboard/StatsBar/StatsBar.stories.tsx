import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import '@/tokens.css'
import { StatsBar } from './StatsBar'

const meta: Meta<typeof StatsBar> = {
  title: 'Dashboard/StatsBar',
  component: StatsBar,
  decorators: [
    (Story) => (
      <MemoryRouter>
        <Story />
      </MemoryRouter>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof StatsBar>

export const ActiveDashboard: Story = {
  args: { activeRuns: 2, pendingApprovals: 1, mcpServerCount: 3, mcpServersLoading: false },
}

export const AllQuiet: Story = {
  args: { activeRuns: 0, pendingApprovals: 0, mcpServerCount: 3, mcpServersLoading: false },
}

export const NoServers: Story = {
  args: { activeRuns: 0, pendingApprovals: 0, mcpServerCount: 0, mcpServersLoading: false },
}

export const Loading: Story = {
  args: { activeRuns: 0, pendingApprovals: 0, mcpServerCount: 0, mcpServersLoading: true },
}

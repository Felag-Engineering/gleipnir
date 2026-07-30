import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import { SetupChecklist } from './SetupChecklist'
import storyStyles from '../dashboard-stories.module.css'

const meta: Meta<typeof SetupChecklist> = {
  title: 'Dashboard/SetupChecklist',
  component: SetupChecklist,
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <div className={storyStyles.storyWrapper}>
            <Story />
          </div>
        </MemoryRouter>
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof SetupChecklist>

export const AllIncomplete: Story = {
  args: {
    hasModel: false,
    hasToolSource: false,
    hasAgent: false,
    hasFirstRun: false,
    isLoading: false,
  },
}

export const ModelDone: Story = {
  args: {
    hasModel: true,
    hasToolSource: false,
    hasAgent: false,
    hasFirstRun: false,
    isLoading: false,
  },
}

export const ToolSourceDone: Story = {
  args: {
    hasModel: true,
    hasToolSource: true,
    hasAgent: false,
    hasFirstRun: false,
    isLoading: false,
  },
}

// Demonstrates that a tool-providing plugin (no MCP server) satisfies the tools step.
export const ToolPluginDone: Story = {
  args: {
    hasModel: true,
    hasToolSource: true,
    hasAgent: false,
    hasFirstRun: false,
    isLoading: false,
  },
}

export const AgentDone: Story = {
  args: {
    hasModel: true,
    hasToolSource: true,
    hasAgent: true,
    hasFirstRun: false,
    isLoading: false,
  },
}

export const Loading: Story = {
  args: {
    hasModel: false,
    hasToolSource: false,
    hasAgent: false,
    hasFirstRun: false,
    isLoading: true,
  },
}
